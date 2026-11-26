// Code generated from Python3Parser.g4 by ANTLR 4.13.2. DO NOT EDIT.

package pyparser // Python3Parser
import "github.com/antlr4-go/antlr/v4"

type BasePython3ParserVisitor struct {
	*antlr.BaseParseTreeVisitor
}

func (v *BasePython3ParserVisitor) VisitSingle_input(ctx *Single_inputContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitFile_input(ctx *File_inputContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitEval_input(ctx *Eval_inputContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDecorator(ctx *DecoratorContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDecorators(ctx *DecoratorsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDecorated(ctx *DecoratedContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAsync_funcdef(ctx *Async_funcdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitFuncdef(ctx *FuncdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitParameters(ctx *ParametersContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTypedargslist(ctx *TypedargslistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTfpdef(ctx *TfpdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitVarargslist(ctx *VarargslistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitVfpdef(ctx *VfpdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStmt(ctx *StmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSimple_stmts(ctx *Simple_stmtsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSimple_stmt(ctx *Simple_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitExpr_stmt(ctx *Expr_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAnnassign(ctx *AnnassignContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTestlist_star_expr(ctx *Testlist_star_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAugassign(ctx *AugassignContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDel_stmt(ctx *Del_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitPass_stmt(ctx *Pass_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitFlow_stmt(ctx *Flow_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitBreak_stmt(ctx *Break_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitContinue_stmt(ctx *Continue_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitReturn_stmt(ctx *Return_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitYield_stmt(ctx *Yield_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitRaise_stmt(ctx *Raise_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImport_stmt(ctx *Import_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImport_name(ctx *Import_nameContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImport_from(ctx *Import_fromContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImport_as_name(ctx *Import_as_nameContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDotted_as_name(ctx *Dotted_as_nameContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImport_as_names(ctx *Import_as_namesContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDotted_as_names(ctx *Dotted_as_namesContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDotted_name(ctx *Dotted_nameContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitGlobal_stmt(ctx *Global_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitNonlocal_stmt(ctx *Nonlocal_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAssert_stmt(ctx *Assert_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitCompound_stmt(ctx *Compound_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAsync_stmt(ctx *Async_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitIf_stmt(ctx *If_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitWhile_stmt(ctx *While_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitFor_stmt(ctx *For_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTry_stmt(ctx *Try_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitWith_stmt(ctx *With_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitWith_item(ctx *With_itemContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitExcept_clause(ctx *Except_clauseContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitBlock(ctx *BlockContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitMatch_stmt(ctx *Match_stmtContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSubject_expr(ctx *Subject_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStar_named_expressions(ctx *Star_named_expressionsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStar_named_expression(ctx *Star_named_expressionContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitCase_block(ctx *Case_blockContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitGuard(ctx *GuardContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitPatterns(ctx *PatternsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitPattern(ctx *PatternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAs_pattern(ctx *As_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitOr_pattern(ctx *Or_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitClosed_pattern(ctx *Closed_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitLiteral_pattern(ctx *Literal_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitLiteral_expr(ctx *Literal_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComplex_number(ctx *Complex_numberContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSigned_number(ctx *Signed_numberContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSigned_real_number(ctx *Signed_real_numberContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitReal_number(ctx *Real_numberContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitImaginary_number(ctx *Imaginary_numberContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitCapture_pattern(ctx *Capture_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitPattern_capture_target(ctx *Pattern_capture_targetContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitWildcard_pattern(ctx *Wildcard_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitValue_pattern(ctx *Value_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAttr(ctx *AttrContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitName_or_attr(ctx *Name_or_attrContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitGroup_pattern(ctx *Group_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSequence_pattern(ctx *Sequence_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitOpen_sequence_pattern(ctx *Open_sequence_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitMaybe_sequence_pattern(ctx *Maybe_sequence_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitMaybe_star_pattern(ctx *Maybe_star_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStar_pattern(ctx *Star_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitMapping_pattern(ctx *Mapping_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitItems_pattern(ctx *Items_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitKey_value_pattern(ctx *Key_value_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDouble_star_pattern(ctx *Double_star_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitClass_pattern(ctx *Class_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitPositional_patterns(ctx *Positional_patternsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitKeyword_patterns(ctx *Keyword_patternsContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitKeyword_pattern(ctx *Keyword_patternContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTest(ctx *TestContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTest_nocond(ctx *Test_nocondContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitLambdef(ctx *LambdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitLambdef_nocond(ctx *Lambdef_nocondContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitOr_test(ctx *Or_testContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAnd_test(ctx *And_testContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitNot_test(ctx *Not_testContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComparison(ctx *ComparisonContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComp_op(ctx *Comp_opContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStar_expr(ctx *Star_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitExpr(ctx *ExprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAtom_expr(ctx *Atom_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitAtom(ctx *AtomContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitName(ctx *NameContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTestlist_comp(ctx *Testlist_compContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTrailer(ctx *TrailerContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSubscriptlist(ctx *SubscriptlistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSubscript_(ctx *Subscript_Context) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitSliceop(ctx *SliceopContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitExprlist(ctx *ExprlistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitTestlist(ctx *TestlistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitDictorsetmaker(ctx *DictorsetmakerContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitClassdef(ctx *ClassdefContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitArglist(ctx *ArglistContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitArgument(ctx *ArgumentContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComp_iter(ctx *Comp_iterContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComp_for(ctx *Comp_forContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitComp_if(ctx *Comp_ifContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitEncoding_decl(ctx *Encoding_declContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitYield_expr(ctx *Yield_exprContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitYield_arg(ctx *Yield_argContext) interface{} {
	return v.VisitChildren(ctx)
}

func (v *BasePython3ParserVisitor) VisitStrings(ctx *StringsContext) interface{} {
	return v.VisitChildren(ctx)
}
