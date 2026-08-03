package openapicheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var ginMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

type sourceFile struct {
	path             string
	prefixes         map[string]string
	functionPrefixes map[string]map[string]string
}

type document struct {
	OpenAPI    string                    `yaml:"openapi"`
	Info       map[string]any            `yaml:"info"`
	Servers    []map[string]any          `yaml:"servers"`
	Paths      map[string]map[string]any `yaml:"paths"`
	Components map[string]any            `yaml:"components"`
}

// 将 OpenAPI 与生产装配可达的 Gin 路由调用对照。通过解析实际路由源码避免维护第二份手写列表，
// 同时校验 operationId 和路径参数，防止接口可访问但文档遗漏或含幽灵接口。
func Check(repositoryRoot, application, specPath string) error {
	files, err := routeFiles(repositoryRoot, application)
	if err != nil {
		return err
	}
	sourceRoutes, err := sourceRoutes(files)
	if err != nil {
		return err
	}
	documentRoutes, err := loadDocument(specPath)
	if err != nil {
		return err
	}
	missing, stale := difference(sourceRoutes, documentRoutes)
	if len(missing) > 0 || len(stale) > 0 {
		return fmt.Errorf("OpenAPI route drift: missing=%v stale=%v", missing, stale)
	}
	return nil
}

func routeFiles(root, application string) ([]sourceFile, error) {
	switch application {
	case "crm":
		return []sourceFile{
			{path: filepath.Join(root, "internal/bootstrap/app.go"), prefixes: map[string]string{"base": "", "api": "/api/v1", "internal": "/api/v1/internal"}},
			{path: filepath.Join(root, "internal/modules/customer/routes.go"), prefixes: map[string]string{"customers": "/api/v1/customers", "router": "/api/v1"}},
			{path: filepath.Join(root, "internal/modules/opportunity/routes.go"), prefixes: map[string]string{"opportunities": "/api/v1/opportunities"}, functionPrefixes: map[string]map[string]string{"RegisterRoutes": {"router": "/api/v1"}, "RegisterIntegrationRoutes": {"router": "/api/v1/internal"}}},
			{path: filepath.Join(root, "internal/modules/ownerdirectory/routes.go"), prefixes: map[string]string{"router": "/api/v1"}},
			{path: filepath.Join(root, "internal/modules/notification/routes.go"), prefixes: map[string]string{"router": "/api/v1"}},
			{path: filepath.Join(root, "internal/modules/portalinvite/routes.go"), prefixes: map[string]string{"api": "/api/v1", "invites": "/api/v1/internal/portal/invites"}},
			{path: filepath.Join(root, "internal/modules/presale/routes.go"), prefixes: map[string]string{"presale": "/api/v1/presale", "internal": "/api/v1/internal"}},
		}, nil
	case "portal":
		return []sourceFile{{path: filepath.Join(root, "internal/portalbootstrap/router.go"), prefixes: map[string]string{"base": "", "internal": "/internal", "api": "/api/v1"}}}, nil
	default:
		return nil, fmt.Errorf("unknown application %q", application)
	}
}

func sourceRoutes(files []sourceFile) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for _, file := range files {
		source, err := os.ReadFile(file.path)
		if err != nil {
			return nil, fmt.Errorf("read route source %s: %w", file.path, err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, source, 0)
		if err != nil {
			return nil, fmt.Errorf("parse route source %s: %w", file.path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			prefixes := make(map[string]string, len(file.prefixes)+2)
			for receiver, prefix := range file.prefixes {
				prefixes[receiver] = prefix
			}
			for receiver, prefix := range file.functionPrefixes[function.Name.Name] {
				prefixes[receiver] = prefix
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				// 路由注册中常用字面量切片循环批量挂载方法；这里对有限字符串表达式求值，遇到动态
				// 计算则失败为“未发现”，避免静态检查器臆测运行时路径。
				rangeStatement, ok := node.(*ast.RangeStmt)
				if !ok {
					if call, callOK := node.(*ast.CallExpr); callOK {
						addCall(result, call, prefixes, nil)
					}
					return true
				}
				values := rangeValues(rangeStatement.X)
				identifier, hasIdentifier := rangeStatement.Value.(*ast.Ident)
				if !hasIdentifier || len(values) == 0 {
					return true
				}
				environment := map[string][]string{identifier.Name: values}
				ast.Inspect(rangeStatement.Body, func(child ast.Node) bool {
					if call, ok := child.(*ast.CallExpr); ok {
						addCall(result, call, prefixes, environment)
					}
					return true
				})
				return false
			})
		}
	}
	return result, nil
}

func addCall(result map[string]struct{}, call *ast.CallExpr, prefixes map[string]string, environment map[string][]string) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !ginMethods[selector.Sel.Name] || len(call.Args) == 0 {
		return
	}
	receiver, ok := selector.X.(*ast.Ident)
	if !ok {
		return
	}
	prefix, ok := prefixes[receiver.Name]
	if !ok {
		return
	}
	for _, routePath := range evaluateStrings(call.Args[0], environment) {
		path := normalizePath(prefix + routePath)
		result[strings.ToLower(selector.Sel.Name)+" "+path] = struct{}{}
	}
}

func rangeValues(expression ast.Expr) []string {
	composite, ok := expression.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(composite.Elts))
	for _, element := range composite.Elts {
		literal, ok := element.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return nil
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil {
			return nil
		}
		values = append(values, value)
	}
	return values
}

func evaluateStrings(expression ast.Expr, environment map[string][]string) []string {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return nil
		}
		decoded, err := strconv.Unquote(value.Value)
		if err != nil {
			return nil
		}
		return []string{decoded}
	case *ast.Ident:
		return environment[value.Name]
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return nil
		}
		left, right := evaluateStrings(value.X, environment), evaluateStrings(value.Y, environment)
		joined := make([]string, 0, len(left)*len(right))
		for _, first := range left {
			for _, second := range right {
				joined = append(joined, first+second)
			}
		}
		return joined
	default:
		return nil
	}
}

var ginParameter = regexp.MustCompile(`:([A-Za-z][A-Za-z0-9_]*)`)

func normalizePath(value string) string {
	value = ginParameter.ReplaceAllString(value, `{$1}`)
	if value == "" {
		return "/"
	}
	return value
}

func loadDocument(path string) (map[string]struct{}, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read OpenAPI %s: %w", path, err)
	}
	var doc document
	if err = yaml.Unmarshal(contents, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI %s: %w", path, err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.1.") || len(doc.Info) == 0 || len(doc.Servers) == 0 || len(doc.Paths) == 0 {
		return nil, fmt.Errorf("%s is not a structurally complete OpenAPI 3.1 document", path)
	}
	routes := make(map[string]struct{})
	operationIDs := make(map[string]string)
	operationParts, _ := doc.Components["x-operations"].(map[string]any)
	for routePath, item := range doc.Paths {
		for method, raw := range item {
			method = strings.ToLower(method)
			if !ginMethods[strings.ToUpper(method)] {
				continue
			}
			operation, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s %s is not an operation object", method, routePath)
			}
			if reference, _ := operation["$ref"].(string); reference != "" {
				const prefix = "#/components/x-operations/"
				if !strings.HasPrefix(reference, prefix) {
					return nil, fmt.Errorf("%s %s has unsupported operation reference %s", method, routePath, reference)
				}
				operation, ok = operationParts[strings.TrimPrefix(reference, prefix)].(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s %s references missing operation %s", method, routePath, reference)
				}
			}
			operationID, _ := operation["operationId"].(string)
			if strings.TrimSpace(operationID) == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, routePath)
			}
			if previous := operationIDs[operationID]; previous != "" {
				return nil, fmt.Errorf("duplicate operationId %s at %s and %s %s", operationID, previous, method, routePath)
			}
			operationIDs[operationID] = method + " " + routePath
			if _, ok = operation["responses"]; !ok {
				return nil, fmt.Errorf("%s %s has no responses", method, routePath)
			}
			if _, ok = operation["security"]; !ok {
				return nil, fmt.Errorf("%s %s does not state its security boundary", method, routePath)
			}
			if err = validatePathParameters(routePath, operation); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, routePath, err)
			}
			routes[method+" "+routePath] = struct{}{}
		}
	}
	return routes, nil
}

var openAPIParameter = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)\}`)

func validatePathParameters(routePath string, operation map[string]any) error {
	expected := openAPIParameter.FindAllStringSubmatch(routePath, -1)
	if len(expected) == 0 {
		return nil
	}
	parameters, _ := operation["parameters"].([]any)
	declared := make(map[string]bool, len(parameters))
	for _, raw := range parameters {
		parameter, _ := raw.(map[string]any)
		if reference, _ := parameter["$ref"].(string); reference != "" {
			parts := strings.Split(reference, "/")
			componentName := parts[len(parts)-1]
			declared[componentName] = true
			continue
		}
		location, _ := parameter["in"].(string)
		name, _ := parameter["name"].(string)
		required, _ := parameter["required"].(bool)
		if location == "path" && name != "" && required {
			declared[name] = true
		}
	}
	for _, match := range expected {
		if declared[match[1]] {
			continue
		}
		matched := false
		for componentName := range declared {
			if strings.EqualFold(componentName, match[1]) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		// 可复用参数可以用语义名称描述类型，而处理器路由常统一使用 id；两者只在明确映射时兼容。
		if match[1] == "id" && (declared["ID"] || declared["NumericID"] || declared["CustomerID"] || declared["OpportunityID"]) {
			continue
		}
		return fmt.Errorf("path parameter %s is not declared required in path", match[1])
	}
	return nil
}

func difference(want, got map[string]struct{}) ([]string, []string) {
	missing, stale := make([]string, 0), make([]string, 0)
	for route := range want {
		if _, ok := got[route]; !ok {
			missing = append(missing, route)
		}
	}
	for route := range got {
		if _, ok := want[route]; !ok {
			stale = append(stale, route)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	return missing, stale
}
