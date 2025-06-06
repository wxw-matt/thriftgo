// Copyright 2021 CloudWeGo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package golang

import (
	"bytes"
	"fmt"
	"go/format"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	"github.com/cloudwego/thriftgo/generator/golang/streaming"
	"github.com/cloudwego/thriftgo/generator/golang/templates/slim"
	"github.com/cloudwego/thriftgo/tool/trimmer/trim"

	ref_tpl "github.com/cloudwego/thriftgo/generator/golang/templates/ref"
	reflection_tpl "github.com/cloudwego/thriftgo/generator/golang/templates/reflection"

	"github.com/cloudwego/thriftgo/generator/backend"
	"github.com/cloudwego/thriftgo/generator/golang/templates"
	"github.com/cloudwego/thriftgo/parser"
	"github.com/cloudwego/thriftgo/plugin"
)

// GoBackend generates go codes.
// The zero value of GoBackend is ready for use.
type GoBackend struct {
	err              error
	tpl              *template.Template
	refTpl           *template.Template
	reflectionTpl    *template.Template
	reflectionRefTpl *template.Template
	req              *plugin.Request
	res              *plugin.Response
	log              backend.LogFunc

	utils *CodeUtils
	funcs template.FuncMap
}

// Name implements the Backend interface.
func (g *GoBackend) Name() string {
	return "go"
}

// Lang implements the Backend interface.
func (g *GoBackend) Lang() string {
	return "Go"
}

// Options implements the Backend interface.
func (g *GoBackend) Options() (opts []plugin.Option) {
	for _, p := range allParams {
		opts = append(opts, plugin.Option{
			Name: p.name,
			Desc: p.desc,
		})
	}
	return opts
}

// BuiltinPlugins implements the Backend interface.
func (g *GoBackend) BuiltinPlugins() []*plugin.Desc {
	return nil
}

// GetPlugin implements the Backend interface.
func (g *GoBackend) GetPlugin(desc *plugin.Desc) plugin.Plugin {
	return nil
}

// Generate implements the Backend interface.
func (g *GoBackend) Generate(req *plugin.Request, log backend.LogFunc) *plugin.Response {
	g.req = req
	g.res = plugin.NewResponse()
	g.log = log
	g.prepareUtilities()
	if g.utils.Features().TrimIDL {
		g.log.Warn("You Are Using IDL Trimmer")
		tr, err := trim.TrimAST(&trim.TrimASTArg{Ast: req.AST, TrimMethods: nil, Preserve: nil})
		if err != nil {
			g.log.Warn("trim error:", err.Error())
			g.err = err
			return g.buildResponse()
		} else {
			g.log.Warn(fmt.Sprintf("removed %d unused structures with %d fields", tr.StructsTrimmed, tr.FieldsTrimmed))

			g.log.Warn(fmt.Sprintf("structs:%d->%d (%.1f%% Trimmed),  fields:%d->%d (%.1f%% Trimmed).", tr.StructsTotal, tr.StructsLeft(), tr.StructTrimmedPercentage(), tr.FieldsTotal, tr.FieldsLeft(), tr.FieldTrimmedPercentage()))
		}
	}
	if !g.utils.Features().ThriftStreaming {
		g.removeStreamingFunctions(req.GetAST())
	}

	if g.utils.Features().SkipGoGen {
		g.log.Warn("You are skipping Thriftgo Go Code Generating")
	} else {
		g.prepareTemplates()
		g.fillRequisitions()
		g.executeTemplates()
	}
	return g.buildResponse()
}

func (g *GoBackend) GetCoreUtils() *CodeUtils {
	return g.utils
}

func (g *GoBackend) prepareUtilities() {
	if g.err != nil {
		return
	}

	g.utils = NewCodeUtils(g.log)
	g.err = g.utils.HandleOptions(g.req.GeneratorParameters)
	if g.err != nil {
		return
	}

	g.funcs = g.utils.BuildFuncMap()
	g.funcs["Version"] = func() string { return g.req.Version }
}

func (g *GoBackend) prepareTemplates() {
	if g.err != nil {
		return
	}

	all := template.New("thrift").Funcs(g.funcs)
	tpls := templates.Templates()

	if g.utils.Features().NoDefaultSerdes {
		tpls = append(templates.Templates(), slim.NoDefaultCodecExtension()...)
	}
	if name := g.utils.Template(); name != defaultTemplate {
		tpls = g.utils.alternative[name]
	}
	for _, tpl := range tpls {
		all = template.Must(all.Parse(tpl))
	}
	g.tpl = all

	g.refTpl = template.Must(template.New("thrift-ref").Funcs(g.funcs).Parse(ref_tpl.File))
	g.reflectionTpl = template.Must(template.New("thrift-reflection").Funcs(g.funcs).Parse(reflection_tpl.File))
	g.reflectionRefTpl = template.Must(template.New("thrift-reflection-util").Funcs(g.funcs).Parse(reflection_tpl.FileRef))
}

func (g *GoBackend) fillRequisitions() {
	if g.err != nil {
		return
	}
}

func (g *GoBackend) executeTemplates() {
	if g.err != nil {
		return
	}

	processed := make(map[*parser.Thrift]bool)

	var trees chan *parser.Thrift
	if g.req.Recursive {
		trees = g.req.AST.DepthFirstSearch()
	} else {
		trees = make(chan *parser.Thrift, 1)
		trees <- g.req.AST
		close(trees)
	}

	for ast := range trees {
		if processed[ast] {
			continue
		}
		processed[ast] = true
		g.log.Info("Processing", ast.Filename)

		if g.err = g.renderOneFile(ast); g.err != nil {
			break
		}
	}
}

func (g *GoBackend) renderOneFile(ast *parser.Thrift) error {
	keepName := g.utils.Features().KeepCodeRefName
	path := g.utils.CombineOutputPath(g.req.OutputPath, ast)
	filename := filepath.Join(path, g.utils.GetFilename(ast))
	localScope, refScope, err := BuildRefScope(g.utils, ast)
	if err != nil {
		return err
	}
	err = g.renderByTemplate(localScope, g.tpl, filename)
	if err != nil {
		return err
	}
	err = g.renderByTemplate(refScope, g.refTpl, ToRefFilename(keepName, filename))
	if err != nil {
		return err
	}
	if g.utils.Features().WithReflection {
		err = g.renderByTemplate(refScope, g.reflectionRefTpl, ToReflectionRefFilename(keepName, filename))
		if err != nil {
			return err
		}
		return g.renderByTemplate(localScope, g.reflectionTpl, ToReflectionFilename(filename))
	}
	return nil
}

func ToRefFilename(keepName bool, filename string) string {
	if keepName {
		return filename
	}
	return strings.TrimSuffix(filename, ".go") + "-ref.go"
}

func ToReflectionFilename(filename string) string {
	return strings.TrimSuffix(filename, ".go") + "-reflection.go"
}

func ToReflectionRefFilename(keepName bool, filename string) string {
	if keepName {
		return ToReflectionFilename(filename)
	}
	return strings.TrimSuffix(filename, ".go") + "-reflection-ref.go"
}

func (s *Scope) IsEmpty() bool {
	if len(s.constants) == 0 &&
		len(s.typedefs) == 0 &&
		len(s.enums) == 0 &&
		len(s.structs) == 0 &&
		len(s.unions) == 0 &&
		len(s.exceptions) == 0 &&
		len(s.services) == 0 &&
		len(s.synthesized) == 0 {
		return true
	}
	return false
}

var poolBuffer = sync.Pool{
	New: func() any {
		p := &bytes.Buffer{}
		p.Grow(100 << 10)
		return p
	},
}

func (g *GoBackend) renderByTemplate(scope *Scope, executeTpl *template.Template, filename string) error {
	if scope == nil {
		return nil
	}
	// if scope has no content, just skip and don't generate this file
	if g.utils.Features().SkipEmpty {
		if scope.IsEmpty() {
			return nil
		}
	}

	w := poolBuffer.Get().(*bytes.Buffer)
	defer poolBuffer.Put(w)

	w.Reset()

	g.utils.SetRootScope(scope)
	err := executeTpl.ExecuteTemplate(w, executeTpl.Name(), scope)
	if err != nil {
		return fmt.Errorf("%s: %w", filename, err)
	}
	g.res.Contents = append(g.res.Contents, &plugin.Generated{
		Content: w.String(),
		Name:    &filename,
	})
	imports, err := scope.ResolveImports()
	if err != nil {
		return err
	}
	w.Reset()
	err = executeTpl.ExecuteTemplate(w, "Imports", imports)
	if err != nil {
		return fmt.Errorf("%s: %w", filename, err)
	}
	point := "imports"
	g.res.Contents = append(g.res.Contents, &plugin.Generated{
		Content:        w.String(),
		InsertionPoint: &point,
	})
	return nil
}

func (g *GoBackend) buildResponse() *plugin.Response {
	if g.err != nil {
		return plugin.BuildErrorResponse(g.err.Error())
	}
	return g.res
}

// PostProcess implements the backend.PostProcessor interface to do
// source formatting before writing files out.
func (g *GoBackend) PostProcess(path string, content []byte) ([]byte, error) {
	if g.utils.Features().NoFmt {
		return content, nil
	}
	switch filepath.Ext(path) {
	case ".go":
		if formated, err := format.Source(content); err != nil {
			g.log.Warn(fmt.Sprintf("Failed to format %s: %s", path, err.Error()))
		} else {
			content = formated
		}
	}
	return content, nil
}

func (g *GoBackend) removeStreamingFunctions(ast *parser.Thrift) {
	for _, svc := range ast.Services {
		functions := make([]*parser.Function, 0, len(svc.Functions))
		for _, f := range svc.Functions {
			st, err := streaming.ParseStreaming(f)
			if err != nil {
				g.log.Warn(fmt.Sprintf("%s.%s: failed to parse streaming, err = %v", svc.Name, f.Name, err))
				continue
			}
			if st.IsStreaming {
				g.log.Warn(fmt.Sprintf("skip streaming function %s.%s: not supported by your kitex, "+
					"please update your kitex tool to the latest version", svc.Name, f.Name))
				continue
			}
			functions = append(functions, f)
		}
		svc.Functions = functions
	}
}
