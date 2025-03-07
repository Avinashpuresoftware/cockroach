// Copyright 2022 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package funcdesc

import (
	"context"

	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/catpb"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/catprivilege"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog/descpb"
	"github.com/cockroachdb/cockroach/pkg/sql/parser"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgcode"
	"github.com/cockroachdb/cockroach/pkg/sql/pgwire/pgerror"
	"github.com/cockroachdb/cockroach/pkg/sql/privilege"
	"github.com/cockroachdb/cockroach/pkg/sql/schemachanger/scpb"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/catid"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/eval"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/volatility"
	"github.com/cockroachdb/cockroach/pkg/sql/types"
	"github.com/cockroachdb/cockroach/pkg/util/hlc"
	"github.com/cockroachdb/errors"
	"github.com/lib/pq/oid"
)

var _ catalog.Descriptor = (*immutable)(nil)
var _ catalog.FunctionDescriptor = (*immutable)(nil)
var _ catalog.FunctionDescriptor = (*Mutable)(nil)
var _ catalog.MutableDescriptor = (*Mutable)(nil)

// immutable represents immutable function descriptor.
type immutable struct {
	descpb.FunctionDescriptor

	// isUncommittedVersion is set to true if this descriptor was created from
	// a copy of a Mutable with an uncommitted version.
	isUncommittedVersion bool

	changes catalog.PostDeserializationChanges
}

// Mutable represents a mutable function descriptor.
type Mutable struct {
	immutable
	clusterVersion *immutable
}

// NewMutableFunctionDescriptor is a mutable function descriptor constructor
// used only with in the legacy schema changer.
func NewMutableFunctionDescriptor(
	id descpb.ID,
	parentID descpb.ID,
	parentSchemaID descpb.ID,
	name string,
	args []descpb.FunctionDescriptor_Argument,
	returnType *types.T,
	returnSet bool,
	privs *catpb.PrivilegeDescriptor,
) Mutable {
	return Mutable{
		immutable: immutable{
			FunctionDescriptor: descpb.FunctionDescriptor{
				Name:           name,
				ID:             id,
				ParentID:       parentID,
				ParentSchemaID: parentSchemaID,
				Args:           args,
				ReturnType: descpb.FunctionDescriptor_ReturnType{
					Type:      returnType,
					ReturnSet: returnSet,
				},
				Lang:              catpb.Function_SQL,
				Volatility:        catpb.Function_VOLATILE,
				LeakProof:         false,
				NullInputBehavior: catpb.Function_CALLED_ON_NULL_INPUT,
				Privileges:        privs,
				Version:           1,
				ModificationTime:  hlc.Timestamp{},
			},
		},
	}
}

// IsUncommittedVersion implements the catalog.LeasableDescriptor interface.
func (desc *immutable) IsUncommittedVersion() bool {
	return desc.isUncommittedVersion
}

// DescriptorType implements the catalog.Descriptor interface.
func (desc *immutable) DescriptorType() catalog.DescriptorType {
	return catalog.Function
}

// GetAuditMode implements the catalog.Descriptor interface.
func (desc *immutable) GetAuditMode() descpb.TableDescriptor_AuditMode {
	return descpb.TableDescriptor_DISABLED
}

// Public implements the catalog.Descriptor interface.
func (desc *immutable) Public() bool {
	return desc.State == descpb.DescriptorState_PUBLIC
}

// Adding implements the catalog.Descriptor interface.
func (desc *immutable) Adding() bool {
	return false
}

// Dropped implements the catalog.Descriptor interface.
func (desc *immutable) Dropped() bool {
	return desc.State == descpb.DescriptorState_DROP
}

// Offline implements the catalog.Descriptor interface.
func (desc *immutable) Offline() bool {
	return desc.State == descpb.DescriptorState_OFFLINE
}

// DescriptorProto implements the catalog.Descriptor interface.
func (desc *immutable) DescriptorProto() *descpb.Descriptor {
	return &descpb.Descriptor{
		Union: &descpb.Descriptor_Function{
			Function: &desc.FunctionDescriptor,
		},
	}
}

// ByteSize implements the catalog.Descriptor interface.
func (desc *immutable) ByteSize() int64 {
	return int64(desc.Size())
}

// GetDeclarativeSchemaChangerState is part of the catalog.MutableDescriptor
// interface.
func (desc *immutable) GetDeclarativeSchemaChangerState() *scpb.DescriptorState {
	return desc.DeclarativeSchemaChangerState.Clone()
}

// NewBuilder implements the catalog.Descriptor interface.
func (desc *Mutable) NewBuilder() catalog.DescriptorBuilder {
	return newBuilder(&desc.FunctionDescriptor, desc.IsUncommittedVersion(), desc.changes)
}

// NewBuilder implements the catalog.Descriptor interface.
func (desc *immutable) NewBuilder() catalog.DescriptorBuilder {
	return newBuilder(&desc.FunctionDescriptor, desc.IsUncommittedVersion(), desc.changes)
}

// GetReferencedDescIDs implements the catalog.Descriptor interface.
func (desc *immutable) GetReferencedDescIDs() (catalog.DescriptorIDSet, error) {
	ret := catalog.MakeDescriptorIDSet(desc.GetID(), desc.GetParentID(), desc.GetParentSchemaID())
	for _, id := range desc.DependsOn {
		ret.Add(id)
	}
	for _, id := range desc.DependsOnTypes {
		ret.Add(id)
	}
	for _, dep := range desc.DependedOnBy {
		ret.Add(dep.ID)
	}

	return ret, nil
}

// ValidateSelf implements the catalog.Descriptor interface.
func (desc *immutable) ValidateSelf(vea catalog.ValidationErrorAccumulator) {
	vea.Report(catalog.ValidateName(desc.Name, "function"))
	if desc.GetID() == descpb.InvalidID {
		vea.Report(errors.AssertionFailedf("invalid ID %d", desc.GetID()))
	}
	if desc.GetParentID() == descpb.InvalidID {
		vea.Report(errors.AssertionFailedf("invalid parentID %d", desc.GetParentID()))
	}
	if desc.GetParentSchemaID() == descpb.InvalidID {
		vea.Report(errors.AssertionFailedf("invalid parentSchemaID %d", desc.GetParentSchemaID()))
	}

	if desc.Privileges == nil {
		vea.Report(errors.AssertionFailedf("privileges not set"))
	} else {
		vea.Report(catprivilege.Validate(*desc.Privileges, desc, privilege.Function))
	}

	// Validate types are properly set.
	if desc.ReturnType.Type == nil {
		vea.Report(errors.AssertionFailedf("return type not set"))
	}
	for i, arg := range desc.Args {
		if arg.Type == nil {
			vea.Report(errors.AssertionFailedf("type not set for arg %d", i))
		}
	}

	vea.Report(CheckLeakProofVolatility(desc))

	for i, dep := range desc.DependedOnBy {
		if dep.ID == descpb.InvalidID {
			vea.Report(errors.AssertionFailedf("invalid relation id %d in depended-on-by references #%d", dep.ID, i))
		}
	}

	for i, depID := range desc.DependsOn {
		if depID == descpb.InvalidID {
			vea.Report(errors.AssertionFailedf("invalid relation id %d in depends-on references #%d", depID, i))
		}
	}

	for i, typeID := range desc.DependsOnTypes {
		if typeID == descpb.InvalidID {
			vea.Report(errors.AssertionFailedf("invalid type id %d in depends-on-types references #%d", typeID, i))
		}
	}
}

// ValidateForwardReferences implements the catalog.Descriptor interface.
func (desc *immutable) ValidateForwardReferences(
	vea catalog.ValidationErrorAccumulator, vdg catalog.ValidationDescGetter,
) {
	// Check that parent DB exists.
	dbDesc, err := vdg.GetDatabaseDescriptor(desc.GetParentID())
	if err != nil {
		vea.Report(err)
	} else if dbDesc.Dropped() {
		vea.Report(errors.AssertionFailedf("parent database %q (%d) is dropped", dbDesc.GetName(), dbDesc.GetID()))
	}

	// Check that parent Schema exists.
	scDesc, err := vdg.GetSchemaDescriptor(desc.GetParentSchemaID())
	if err != nil {
		vea.Report(err)
	} else if scDesc.Dropped() {
		vea.Report(errors.AssertionFailedf("parent schema %q (%d) is dropped", scDesc.GetName(), scDesc.GetID()))
	}

	for _, depID := range desc.DependsOn {
		vea.Report(catalog.ValidateOutboundTableRef(depID, vdg))
	}

	for _, typeID := range desc.DependsOnTypes {
		vea.Report(catalog.ValidateOutboundTypeRef(typeID, vdg))
	}
}

// ValidateBackReferences implements the catalog.Descriptor interface.
func (desc *immutable) ValidateBackReferences(
	vea catalog.ValidationErrorAccumulator, vdg catalog.ValidationDescGetter,
) {
	// Check that function exists in parent schema.
	if sc, err := vdg.GetSchemaDescriptor(desc.GetParentSchemaID()); err == nil {
		vea.Report(desc.validateFuncExistsInSchema(sc))
	}

	for _, depID := range desc.DependsOn {
		tbl, _ := vdg.GetTableDescriptor(depID)
		vea.Report(catalog.ValidateOutboundTableRefBackReference(desc.GetID(), tbl))
	}

	for _, typeID := range desc.DependsOnTypes {
		typ, _ := vdg.GetTypeDescriptor(typeID)
		vea.Report(catalog.ValidateOutboundTypeRefBackReference(desc.GetID(), typ))
	}

	// Currently, we don't support cross function references yet.
	// So here we assume that all inbound references are from tables.
	for _, by := range desc.DependedOnBy {
		vea.Report(desc.validateInboundTableRef(by, vdg))
	}
}

func (desc *immutable) validateFuncExistsInSchema(scDesc catalog.SchemaDescriptor) error {
	// Check that parent Schema contains the matching function signature.
	if _, ok := scDesc.GetFunction(desc.GetName()); !ok {
		return errors.AssertionFailedf("function does not exist in schema %q (%d)",
			scDesc.GetName(), scDesc.GetID())
	}

	function, _ := scDesc.GetFunction(desc.GetName())
	for _, overload := range function.Overloads {
		// TODO (Chengxiong) maybe a overkill, but we could also validate function
		// signature matches.
		if overload.ID == desc.GetID() {
			return nil
		}
	}
	return errors.AssertionFailedf("function overload %q (%d) cannot be found in schema %q (%d)",
		desc.GetName(), desc.GetID(), scDesc.GetName(), scDesc.GetID())
}

func (desc *immutable) validateInboundTableRef(
	by descpb.FunctionDescriptor_Reference, vdg catalog.ValidationDescGetter,
) error {
	backRefTbl, err := vdg.GetTableDescriptor(by.ID)
	if err != nil {
		return errors.NewAssertionErrorWithWrappedErrf(err, "invalid depended-on-by relation back reference")
	}
	if backRefTbl.Dropped() {
		return errors.AssertionFailedf("depended-on-by relation %q (%d) is dropped",
			backRefTbl.GetName(), backRefTbl.GetID())
	}

	for _, colID := range by.ColumnIDs {
		_, err := backRefTbl.FindColumnWithID(colID)
		if err != nil {
			return errors.AssertionFailedf("depended-on-by relation %q (%d) does not have a column with ID %d",
				backRefTbl.GetName(), by.ID, colID)
		}
	}

	for _, idxID := range by.IndexIDs {
		_, err := backRefTbl.FindIndexWithID(idxID)
		if err != nil {
			return errors.AssertionFailedf("depended-on-by relation %q (%d) does not have an index with ID %d",
				backRefTbl.GetName(), by.ID, idxID)
		}
	}

	for _, cstID := range by.ConstraintIDs {
		_, err := backRefTbl.FindConstraintWithID(cstID)
		if err != nil {
			return errors.AssertionFailedf("depended-on-by relation %q (%d) does not have a constraint with ID %d",
				backRefTbl.GetName(), by.ID, cstID)
		}
	}

	for _, id := range backRefTbl.GetDependsOn() {
		if id == desc.GetID() {
			return nil
		}
	}
	return errors.AssertionFailedf("depended-on-by table %q (%d) has no corresponding depends-on forward reference",
		backRefTbl.GetName(), by.ID)
}

// ValidateTxnCommit implements the catalog.Descriptor interface.
func (desc *immutable) ValidateTxnCommit(
	vea catalog.ValidationErrorAccumulator, vdg catalog.ValidationDescGetter,
) {
	// No-op
}

// GetPostDeserializationChanges implements the catalog.Descriptor interface.
func (desc *immutable) GetPostDeserializationChanges() catalog.PostDeserializationChanges {
	return desc.changes
}

// HasConcurrentSchemaChanges implements the catalog.Descriptor interface.
func (desc *immutable) HasConcurrentSchemaChanges() bool {
	return desc.DeclarativeSchemaChangerState != nil &&
		desc.DeclarativeSchemaChangerState.JobID != catpb.InvalidJobID
}

// SkipNamespace implements the catalog.Descriptor interface.
func (desc *immutable) SkipNamespace() bool {
	return true
}

// IsUncommittedVersion implements the catalog.LeasableDescriptor interface.
func (desc *Mutable) IsUncommittedVersion() bool {
	return desc.IsNew() || desc.clusterVersion.GetVersion() != desc.GetVersion()
}

// MaybeIncrementVersion implements the catalog.MutableDescriptor interface.
func (desc *Mutable) MaybeIncrementVersion() {
	// Already incremented, no-op.
	if desc.clusterVersion == nil || desc.Version == desc.clusterVersion.Version+1 {
		return
	}
	desc.Version++
	desc.ModificationTime = hlc.Timestamp{}
}

// OriginalName implements the catalog.MutableDescriptor interface.
func (desc *Mutable) OriginalName() string {
	if desc.clusterVersion == nil {
		return ""
	}
	return desc.clusterVersion.Name
}

// OriginalID implements the catalog.MutableDescriptor interface.
func (desc *Mutable) OriginalID() descpb.ID {
	if desc.clusterVersion == nil {
		return descpb.InvalidID
	}
	return desc.clusterVersion.ID
}

// OriginalVersion implements the catalog.MutableDescriptor interface.
func (desc *Mutable) OriginalVersion() descpb.DescriptorVersion {
	if desc.clusterVersion == nil {
		return 0
	}
	return desc.clusterVersion.Version
}

// ImmutableCopy implements the catalog.MutableDescriptor interface.
func (desc *Mutable) ImmutableCopy() catalog.Descriptor {
	return desc.NewBuilder().BuildImmutable()
}

// IsNew implements the catalog.MutableDescriptor interface.
func (desc *Mutable) IsNew() bool {
	return desc.clusterVersion == nil
}

// SetPublic implements the catalog.MutableDescriptor interface.
func (desc *Mutable) SetPublic() {
	desc.State = descpb.DescriptorState_PUBLIC
	desc.OfflineReason = ""
}

// SetDropped implements the catalog.MutableDescriptor interface.
func (desc *Mutable) SetDropped() {
	desc.State = descpb.DescriptorState_DROP
	desc.OfflineReason = ""
}

// SetOffline implements the catalog.MutableDescriptor interface.
func (desc *Mutable) SetOffline(reason string) {
	desc.State = descpb.DescriptorState_OFFLINE
	desc.OfflineReason = reason
}

// SetDeclarativeSchemaChangerState implements the catalog.MutableDescriptor interface.
func (desc *Mutable) SetDeclarativeSchemaChangerState(state *scpb.DescriptorState) {
	desc.DeclarativeSchemaChangerState = state
}

// AddArguments adds function arguments to argument list.
func (desc *Mutable) AddArguments(args ...descpb.FunctionDescriptor_Argument) {
	desc.Args = append(desc.Args, args...)
}

// SetVolatility sets the volatility attribute.
func (desc *Mutable) SetVolatility(v catpb.Function_Volatility) {
	desc.Volatility = v
}

// SetLeakProof sets the leakproof attribute.
func (desc *Mutable) SetLeakProof(v bool) {
	desc.LeakProof = v
}

// SetNullInputBehavior sets the NullInputBehavior attribute.
func (desc *Mutable) SetNullInputBehavior(v catpb.Function_NullInputBehavior) {
	desc.NullInputBehavior = v
}

// SetLang sets the function language.
func (desc *Mutable) SetLang(v catpb.Function_Language) {
	desc.Lang = v
}

// SetFuncBody sets the function body.
func (desc *Mutable) SetFuncBody(v string) {
	desc.FunctionBody = v
}

// SetName sets the function name.
func (desc *Mutable) SetName(n string) {
	desc.Name = n
}

// SetParentSchemaID sets function's parent schema id.
func (desc *Mutable) SetParentSchemaID(id descpb.ID) {
	desc.ParentSchemaID = id
}

// ToFuncObj converts the descriptor to a tree.FuncObj.
func (desc *immutable) ToFuncObj() tree.FuncObj {
	ret := tree.FuncObj{
		FuncName: tree.MakeFunctionNameFromPrefix(tree.ObjectNamePrefix{}, tree.Name(desc.Name)),
		Args:     make(tree.FuncArgs, len(desc.Args)),
	}
	for i := range desc.Args {
		ret.Args[i] = tree.FuncArg{
			Type: desc.Args[i].Type,
		}
	}
	return ret
}

// GetObjectType implements the PrivilegeObject interface.
func (desc *immutable) GetObjectType() privilege.ObjectType {
	return privilege.Function
}

// GetPrivilegeDescriptor implements the PrivilegeObject interface.
func (desc *immutable) GetPrivilegeDescriptor(
	ctx context.Context, planner eval.Planner,
) (*catpb.PrivilegeDescriptor, error) {
	return desc.GetPrivileges(), nil
}

// FuncDesc implements the catalog.FunctionDescriptor interface.
func (desc *immutable) FuncDesc() *descpb.FunctionDescriptor {
	return &desc.FunctionDescriptor
}

// GetLanguage implements the FunctionDescriptor interface.
func (desc *immutable) GetLanguage() catpb.Function_Language {
	return desc.Lang
}

// ContainsUserDefinedTypes implements the catalog.HydratableDescriptor interface.
func (desc *immutable) ContainsUserDefinedTypes() bool {
	for i := range desc.Args {
		if desc.Args[i].Type.UserDefined() {
			return true
		}
	}
	return desc.ReturnType.Type.UserDefined()
}

func (desc *immutable) ToOverload() (ret *tree.Overload, err error) {
	ret = &tree.Overload{
		Oid:        catid.FuncIDToOID(desc.ID),
		ReturnType: tree.FixedReturnType(desc.ReturnType.Type),
		ReturnSet:  desc.ReturnType.ReturnSet,
		Body:       desc.FunctionBody,
		IsUDF:      true,
	}

	argTypes := make(tree.ArgTypes, 0, len(desc.Args))
	for _, arg := range desc.Args {
		argTypes = append(
			argTypes,
			tree.ArgType{Name: arg.Name, Typ: arg.Type},
		)
	}
	ret.Types = argTypes
	ret.Volatility, err = desc.getOverloadVolatility()
	if err != nil {
		return nil, err
	}
	ret.CalledOnNullInput, err = desc.calledOnNullInput()
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (desc *immutable) getOverloadVolatility() (volatility.V, error) {
	var ret volatility.V
	switch desc.Volatility {
	case catpb.Function_VOLATILE:
		ret = volatility.Volatile
	case catpb.Function_STABLE:
		ret = volatility.Stable
	case catpb.Function_IMMUTABLE:
		ret = volatility.Immutable
	default:
		return 0, errors.Newf("unknown volatility")
	}
	if desc.LeakProof {
		if desc.Volatility != catpb.Function_IMMUTABLE {
			return 0, errors.Newf("function %d is leakproof but not immutable", desc.ID)
		}
		ret = volatility.Leakproof
	}
	return ret, nil
}

// calledOnNullInput returns true if the function should be called when any of
// its input arguments are NULL. See Overload.CalledOnNullInput for more
// details.
func (desc *immutable) calledOnNullInput() (bool, error) {
	switch desc.NullInputBehavior {
	case catpb.Function_CALLED_ON_NULL_INPUT:
		return true, nil
	case catpb.Function_RETURNS_NULL_ON_NULL_INPUT, catpb.Function_STRICT:
		return false, nil
	default:
		return false, errors.Newf("unknown null input behavior")
	}
}

// ToCreateExpr implements the FunctionDescriptor interface.
func (desc *immutable) ToCreateExpr() (ret *tree.CreateFunction, err error) {
	ret = &tree.CreateFunction{
		FuncName: tree.MakeFunctionNameFromPrefix(tree.ObjectNamePrefix{}, tree.Name(desc.Name)),
		ReturnType: tree.FuncReturnType{
			Type:  desc.ReturnType.Type,
			IsSet: desc.ReturnType.ReturnSet,
		},
	}
	ret.Args = make(tree.FuncArgs, len(desc.Args))
	for i := range desc.Args {
		ret.Args[i] = tree.FuncArg{
			Name:  tree.Name(desc.Args[i].Name),
			Type:  desc.Args[i].Type,
			Class: toTreeNodeArgClass(desc.Args[i].Class),
		}
		if desc.Args[i].DefaultExpr != nil {
			ret.Args[i].DefaultVal, err = parser.ParseExpr(*desc.Args[i].DefaultExpr)
			if err != nil {
				return nil, err
			}
		}
	}
	// We only store 5 function attributes at the moment. We may extend the
	// pre-allocated capacity in the future.
	ret.Options = make(tree.FunctionOptions, 0, 5)
	ret.Options = append(ret.Options, desc.getCreateExprVolatility())
	ret.Options = append(ret.Options, tree.FunctionLeakproof(desc.LeakProof))
	ret.Options = append(ret.Options, desc.getCreateExprNullInputBehavior())
	ret.Options = append(ret.Options, tree.FunctionBodyStr(desc.FunctionBody))
	ret.Options = append(ret.Options, desc.getCreateExprLang())
	return ret, nil
}

func (desc *immutable) getCreateExprLang() tree.FunctionLanguage {
	switch desc.Lang {
	case catpb.Function_SQL:
		return tree.FunctionLangSQL
	}
	return 0
}

func (desc *immutable) getCreateExprVolatility() tree.FunctionVolatility {
	switch desc.Volatility {
	case catpb.Function_IMMUTABLE:
		return tree.FunctionImmutable
	case catpb.Function_STABLE:
		return tree.FunctionStable
	case catpb.Function_VOLATILE:
		return tree.FunctionVolatile
	}
	return 0
}

func (desc *immutable) getCreateExprNullInputBehavior() tree.FunctionNullInputBehavior {
	switch desc.NullInputBehavior {
	case catpb.Function_CALLED_ON_NULL_INPUT:
		return tree.FunctionCalledOnNullInput
	case catpb.Function_RETURNS_NULL_ON_NULL_INPUT:
		return tree.FunctionReturnsNullOnNullInput
	case catpb.Function_STRICT:
		return tree.FunctionStrict
	}
	return 0
}

func toTreeNodeArgClass(class catpb.Function_Arg_Class) tree.FuncArgClass {
	switch class {
	case catpb.Function_Arg_IN:
		return tree.FunctionArgIn
	case catpb.Function_Arg_OUT:
		return tree.FunctionArgOut
	case catpb.Function_Arg_IN_OUT:
		return tree.FunctionArgInOut
	case catpb.Function_Arg_VARIADIC:
		return tree.FunctionArgVariadic
	}
	return 0
}

// UserDefinedFunctionOIDToID converts a UDF OID into a descriptor ID. OID of a
// UDF must be greater CockroachPredefinedOIDMax. The function returns an error
// if the given OID is less than or equal to CockroachPredefinedOIDMax.
func UserDefinedFunctionOIDToID(oid oid.Oid) (descpb.ID, error) {
	return catid.UserDefinedOIDToID(oid)
}

// IsOIDUserDefinedFunc returns true if an oid is a user-defined function oid.
func IsOIDUserDefinedFunc(oid oid.Oid) bool {
	return catid.IsOIDUserDefined(oid)
}

// CheckLeakProofVolatility returns an error when a function is defined as
// leakproof but not immutable. See more details in comments for volatility.V.
func CheckLeakProofVolatility(fn catalog.FunctionDescriptor) error {
	if fn.GetLeakProof() && fn.GetVolatility() != catpb.Function_IMMUTABLE {
		return pgerror.Newf(
			pgcode.InvalidFunctionDefinition,
			"cannot set leakproof on function with non-immutable volatility: %s",
			fn.GetVolatility().String(),
		)
	}
	return nil
}
