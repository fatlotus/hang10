package hang10

import (
	"fmt"
	"io"
)

type stmtWithCondition struct {
	Cond      condition
	Statement generatedStatement
}

type generator struct {
	Name          string
	Conditions    []stmtWithCondition
	Locals        map[string]register
	Results       int
	Registers     []*Kind
	IsNative      bool
	ReturnKind    []*Kind
	Substitutions map[register]register
	ChildCalls    []string
	NextClosure   int

	CurrentCondition condition
	NextCondition    condition
}

func newGenerator(name string, program *program, argNames []string, argKinds []*Kind, results []*Kind) *generator {
	function := &generator{}
	function.Name = name
	function.Substitutions = map[register]register{}
	function.Locals = map[string]register{}
	function.ReturnKind = results
	function.Results = len(results)

	function.Conditions = []stmtWithCondition{}
	function.CurrentCondition = 0

	for i, arg := range argNames {
		function.Registers = append(function.Registers, argKinds[i])
		function.Locals[arg] = register(i)
	}

	program.GeneratedFunctions = append(program.GeneratedFunctions, function)
	return function
}

func (g generator) ResolveRegister(r register) register {
	for {
		r2, ok := g.Substitutions[r]
		if !ok {
			return r
		}
		if r2 >= r {
			panic("Substitutions should always decrease")
		}
		r = r2
	}
}

func (g generator) Reg(r register) string {
	return fmt.Sprintf("sp->r[%d]", g.ResolveRegister(r))
}

func (g *generator) JoinRegisters(a, b register) {
	a = g.ResolveRegister(a)
	b = g.ResolveRegister(b)
	if a == b {
		return
	}
	if a < b {
		g.Substitutions[b] = a
	} else {
		g.Substitutions[a] = b
	}
}

func (g *generator) Stmt(s generatedStatement) {
	g.StmtWithCond(g.CurrentCondition, s)
}

func (g *generator) StmtWithCond(c condition, s generatedStatement) {
	g.Conditions = append(g.Conditions, stmtWithCondition{c, s})
}

func (g *generator) NewReg(k *Kind, immediate bool) register {
	reg := register(len(g.Registers))
	g.Registers = append(g.Registers, k)
	return reg
}

func (g *generator) CopyOfLocals() map[string]register {
	result := map[string]register{}
	for name, reg := range g.Locals {
		result[name] = reg
	}
	return result
}

func (g *generator) DeleteUnreferenced() {
	referenced := map[register]bool{}
	for _, reg := range g.Locals {
		referenced[reg] = true
	}
	for index, kind := range g.Registers {
		reg := register(index)
		if kind != nil && !referenced[reg] {
			if !kind.Borrowed && kind.Family == FamilyString {
				g.Stmt(&genFree{reg})
				g.Registers[reg] = nil
			}
		}
	}
}

func (g generator) TypeDefinition(w io.Writer) {
	if g.IsNative {
		fmt.Fprintf(w, "void hang10_%s();\n", g.Name)
		return
	}
	fmt.Fprintf(w, "struct hang10_%s_state {\n", g.Name)
	fmt.Fprintf(w, "  future_t r[%d];\n", len(g.Registers))
	fmt.Fprintf(w, "  future_t *result[%d];\n", g.Results)
	fmt.Fprintf(w, "  closure_t caller;\n")
	fmt.Fprintf(w, "  bool conditions[%d];\n", len(g.Conditions))
	for index, kind := range g.ChildCalls {
		fmt.Fprintf(w, "  struct hang10_%s_state *call_%d;\n", kind, index)
		fmt.Fprintf(w, "  bool call_%d_done;\n", index)
	}
	fmt.Fprintf(w, "};\n")
	fmt.Fprintf(w, "%s;\n", g.Header())
}

func (g *generator) DumpRegisters(w io.Writer) {
	fmt.Fprintf(w, "  printf(\"%s %%p ready=", g.Name)
	for range g.Registers {
		fmt.Fprintf(w, "%%s")
	}
	fmt.Fprintf(w, "\\n\", sp")

	for i := range g.Registers {
		fmt.Fprintf(w, ", (%s.ready ? \"r%d \" : \"\")", g.Reg(register(i)), i)
	}
	fmt.Fprintf(w, ");\n")
}

func (g *generator) FormatInto(w io.Writer) {
	if g.IsNative {
		return
	}

	fmt.Fprintf(w, "%s {\n", g.Header())

	fmt.Fprintf(w, "  if (!sp->conditions[0]) {\n")
	fmt.Fprintf(w, "    memset(&sp->conditions, '\\0', sizeof(sp->conditions));\n")
	fmt.Fprintf(w, "    sp->conditions[0] = true;\n")
	for i := range g.ChildCalls {
		fmt.Fprintf(w, "    sp->call_%d = NULL;\n", i)
		fmt.Fprintf(w, "    sp->call_%d_done = false;\n", i)
	}
	fmt.Fprintf(w, "  }\n")

	// g.DumpRegisters(w)

	for _, stmtWithCondition := range g.Conditions {
		condition := stmtWithCondition.Cond
		stmt := stmtWithCondition.Statement

		fmt.Fprintf(w, "  // %#v\n", stmt)

		if condition > 0 {
			fmt.Fprintf(w, "  if (sp->conditions[%d]", condition)
		} else {
			fmt.Fprintf(w, "  if (true")
		}

		needs, provides := stmt.Deps()
		for _, need := range needs {
			fmt.Fprintf(w, " && %s.ready", g.Reg(need))
		}
		for _, provide := range provides {
			fmt.Fprintf(w, " && !%s.ready", g.Reg(provide))
		}
		fmt.Fprintf(w, ") {\n")

		fmt.Fprintf(w, "%s", stmt.Generate(g))
		fmt.Fprintf(w, "  }\n")
	}

	// g.DumpRegisters(w)

	fmt.Fprintf(w, "}\n")
}

func (g generator) Header() string {
	return fmt.Sprintf("void hang10_%s(struct hang10_runtime *rt, struct hang10_%s_state *sp)", g.Name, g.Name)
}

func (g *generator) NewClosure(p *program, argNames []string, argKinds []*Kind, results []*Kind) *generator {
	g.NextClosure += 1
	return newGenerator(fmt.Sprintf("%s_%d", g.Name, g.NextClosure), p, argNames, argKinds, results)
}

func (g *generator) NewCondition() condition {
	g.NextCondition += 1
	return g.NextCondition
}

func (g *generator) NewChildCall(name string) childCall {
	g.ChildCalls = append(g.ChildCalls, name)
	return childCall(len(g.ChildCalls) - 1)
}