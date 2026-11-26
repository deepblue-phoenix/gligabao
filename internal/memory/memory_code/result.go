package memory_code

// Result is everything that parsing a file gives, for any language: one struct for Python, TypeScript, JavaScript and CSS. Fields a
// language does not have are empty (Types and Annotations only come from TypeScript, Styles only from CSS).
//
// Which parser makes it: TypeScript is parsed only by typescript-go, the official TypeScript parser, because esbuild's tree has no type syntax
// (its parser skips it; a type declaration is an empty STypeScript statement). One walk of that tree gives the graph (code and types) and the
// Types and Annotations. JavaScript and CSS are parsed by esbuild. Python by ANTLR4.
type Result struct {
	Language string
	Graph    Graph
	// Types are the type declarations (TypeScript): interfaces, type aliases and enums, with their members.
	Types []TypeDecl
	// Annotations are the type annotations of parameters, return values, variables and class properties (TypeScript).
	Annotations []Annotation
	// Styles are the style rules (CSS), in source order.
	Styles []StyleRule
}

// Kinds of the TypeDecl.
const (
	TypeDeclInterface = "interface"
	TypeDeclAlias     = "type_alias"
	TypeDeclEnum      = "enum"
	TypeDeclConstEnum = "const_enum"
)

// TypeDecl is an interface, type alias or enum. Names are qualified like the entities of the graph ("Geo.Point").
type TypeDecl struct {
	Kind       string
	Name       string
	Exported   bool
	TypeParams []string // as written: "T extends Base = Default"
	Extends    []string // interfaces: the names of the extended types, as written (imports resolved)
	// Type is the right hand side of a type alias, as written.
	Type    string
	Members []TypeMember
}

// Kinds of the TypeMember.
const (
	MemberProperty  = "property"
	MemberMethod    = "method"
	MemberCall      = "call"
	MemberConstruct = "construct"
	MemberIndex     = "index"
	MemberEnum      = "enum_member"
)

// TypeMember is a member of an interface or enum. Type is the type of a property, the signature of a method, call, construct or index
// signature, or the value of an enum member (empty if it has none).
type TypeMember struct {
	Kind     string
	Name     string
	Type     string
	Optional bool
	Readonly bool
}

// Roles of the Annotation.
const (
	RoleParam    = "param"
	RoleReturn   = "return"
	RoleVariable = "variable"
	RoleProperty = "property"
)

// Annotation is a type annotation written in the code. Owner is the qualified name of the function, method or class the annotation
// belongs to (the scope for a variable); Name is the parameter, variable or property (empty for a return type).
type Annotation struct {
	Owner    string
	Role     string
	Name     string
	Type     string
	Optional bool
}

// StyleRule is a CSS rule with declarations. Context are the enclosing at-rules and rules, outermost first ("@media (min-width: 1px)").
type StyleRule struct {
	Selectors    []string
	Context      []string
	Declarations []StyleDecl
}

// StyleDecl is a CSS declaration.
type StyleDecl struct {
	Property  string
	Value     string
	Important bool
}

func parsePython(text string) (Result, error) {
	g, err := extractPython(text)
	return Result{
		Language: LangPython,
		Graph:    g,
	}, err
}
