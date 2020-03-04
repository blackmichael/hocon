package hocon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/scanner"
)

//type TokenType string

const (
	equalsToken      = "="
	commaToken       = ","
	colonToken       = ":"
	dotToken         = "."
	objectStartToken = "{"
	objectEndToken   = "}"
	arrayStartToken  = "["
	arrayEndToken    = "]"
	plusEqualsToken  = "+="
	includeToken     = "include"
)

var forbiddenCharacters = map[string]bool{
	"$": true, `"`: true, objectStartToken: true, objectEndToken: true, arrayStartToken: true, arrayEndToken: true,
	colonToken: true, equalsToken: true, commaToken: true, "+": true, "#": true, "`": true, "^": true, "?": true,
	"!": true, "@": true, "*": true, "&": true, `\`: true, "(": true, ")": true,
}

type Parser struct {
	scanner *scanner.Scanner
}

func newParser(src io.Reader) *Parser {
	s := new(scanner.Scanner)
	s.Init(src)
	s.Error = func(*scanner.Scanner, string) {} // do not print errors to stderr
	return &Parser{scanner:s}
}

func ParseString(input string) (*Config, error) {
	parser := newParser(strings.NewReader(input))
	return parser.parse()
}

func ParseResource(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not parse resource: %w", err)
	}
	return newParser(file).parse()
}

func (p *Parser) parse() (*Config, error) {
	p.scanner.Scan()
	if p.scanner.TokenText() == arrayStartToken {
		configArray, err := p.extractConfigArray()
		if err != nil {
			return nil, err
		}
		return &Config{root:configArray}, nil
	}

	configObject, err := p.extractConfigObject()
	if err != nil {
		return nil, err
	}
	if token := p.scanner.TokenText(); token != "" {
		return nil, invalidConfigObject("invalid token " + token, p.scanner.Position.Line, p.scanner.Column)
	}
	err = resolveSubstitutions(configObject)
	if err != nil {
		return nil, err
	}
	return &Config{root:configObject}, nil
}

func resolveSubstitutions(root *ConfigObject, configValueOptional ...ConfigValue) error {
	var configValue ConfigValue
	if configValueOptional == nil {
		configValue = root
	} else {
		configValue = configValueOptional[0]
	}

	switch v := configValue.(type) {
	case *ConfigArray:
		for i, value := range v.values {
			err := processSubstitution(root, value, func(foundValue ConfigValue) { v.values[i] = foundValue })
			if err != nil {
				return err
			}
		}
	case *ConfigObject:
		for key, value := range v.items {
			err := processSubstitution(root, value, func(foundValue ConfigValue) { v.items[key] = foundValue })
			if err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid type for substitution, substitutions are only allowed in field values and array elements")
	}
	return nil
}

func processSubstitution(root *ConfigObject, value ConfigValue, resolveFunc func(configValue ConfigValue)) error {
	if value.ValueType() == ValueTypeSubstitution {
		substitution := value.(*Substitution)
		if foundValue := root.find(substitution.path); foundValue != nil {
			resolveFunc(foundValue)
		} else if !substitution.optional {
			return errors.New("could not resolve substitution: " + substitution.String() + " to a value")
		}
	} else if valueType := value.ValueType(); valueType == ValueTypeObject || valueType == ValueTypeArray {
		return resolveSubstitutions(root, value)
	}
	return nil
}

func (p *Parser) extractConfigObject() (*ConfigObject, error) {
	root := map[string]ConfigValue{}
	parenthesisBalanced := true

	if p.scanner.TokenText() == objectStartToken {
		parenthesisBalanced = false
		p.scanner.Scan()
		if !parenthesisBalanced && p.scanner.TokenText() == objectEndToken {
			parenthesisBalanced = true
			p.scanner.Scan()
			return NewConfigObject(root), nil
		}
	}
	for tok := p.scanner.Peek(); tok != scanner.EOF; tok = p.scanner.Peek() {
		if p.scanner.TokenText() == includeToken {
			p.scanner.Scan()
			includedConfigObject, err := p.parseIncludedResource()
			if err != nil {
				return nil, err
			}
			mergeConfigObjects(root, includedConfigObject)
			p.scanner.Scan()
		}

		key := p.scanner.TokenText()
		if forbiddenCharacters[key] {
			return nil, fmt.Errorf("invalid key! %q is a forbidden character in keys", key)
		}
		if key == dotToken {
			return nil, leadingPeriodError(p.scanner.Position.Line, p.scanner.Position.Column)
		}
		p.scanner.Scan()
		text := p.scanner.TokenText()

		if text == dotToken || text == objectStartToken {
			if text == dotToken {
				p.scanner.Scan() // skip "."
				if p.scanner.TokenText() == dotToken {
					return nil, adjacentPeriodsError(p.scanner.Position.Line, p.scanner.Position.Column)
				}
				if isSeparator(p.scanner.TokenText(), p.scanner.Peek()) {
					return nil, trailingPeriodError(p.scanner.Position.Line, p.scanner.Position.Column - 1)
				}
			}
			configObject, err := p.extractConfigObject()
			if err != nil {
				return nil, err
			}
			root[key] = configObject
		}

		switch text {
		case equalsToken, colonToken:
			currentRune := p.scanner.Scan()
			configValue, err := p.extractConfigValue(currentRune)
			if err != nil {
				return nil, err
			}

			if configObject, ok := configValue.(*ConfigObject); ok {
				if existingConfigObject, ok := root[key].(*ConfigObject); ok {
					mergeConfigObjects(existingConfigObject.items, configObject)
					configValue = existingConfigObject
				}
			}
			root[key] = configValue
		case "+" :
			if p.scanner.Peek() == '=' {
				p.scanner.Scan()
				currentRune := p.scanner.Scan()
				err := p.parsePlusEqualsValue(root, key, currentRune)
				if err != nil {
					return nil, err
				}
			}
		}

		if p.scanner.TokenText() == commaToken {
			p.scanner.Scan() // skip ","
		}

		if !parenthesisBalanced && p.scanner.TokenText() == objectEndToken {
			parenthesisBalanced = true
			p.scanner.Scan()
			break
		}
	}

	if !parenthesisBalanced {
		return nil, invalidConfigObject("parenthesis do not match", p.scanner.Position.Line, p.scanner.Position.Column)
	}
	return NewConfigObject(root), nil
}

func mergeConfigObjects(existingItems map[string]ConfigValue, new *ConfigObject) {
	for key, value := range new.items {
		existingValue, ok := existingItems[key]
		if ok && existingValue.ValueType() == ValueTypeObject && value.ValueType() == ValueTypeObject {
			existingObj := existingValue.(*ConfigObject)
			mergeConfigObjects(existingObj.items, value.(*ConfigObject))
			value = existingObj
		}
		existingItems[key] = value
	}
}

func (p *Parser) parsePlusEqualsValue(existingItems map[string]ConfigValue, key string, currentRune rune) error {
	existing, ok := existingItems[key]
	if !ok {
		configValue, err := p.extractConfigValue(currentRune)
		if err != nil {
			return err
		}
		existingItems[key] = NewConfigArray([]ConfigValue{configValue})
	} else {
		existingArray, ok := existing.(*ConfigArray)
		if !ok {
			return fmt.Errorf("value: %q of the key: %q is not an array", existing.String(), key)
		}
		configValue, err := p.extractConfigValue(currentRune)
		if err != nil {
			return err
		}
		existingArray.Append(configValue)
	}
	return nil
}

func (p *Parser) validateIncludeValue() (*IncludeToken, error) {
	var required bool
	token := p.scanner.TokenText()
	if token == "required" {
		required = true
		p.scanner.Scan()
		if p.scanner.TokenText() != "(" {
			return nil, errors.New("invalid include value! missing opening parenthesis")
		}
		p.scanner.Scan()
		token = p.scanner.TokenText()
	}
	if token == "file" || token == "classpath" {
		p.scanner.Scan()
		if p.scanner.TokenText() != "(" {
			return nil, errors.New("invalid include value! missing opening parenthesis")
		}
		p.scanner.Scan()
		path := p.scanner.TokenText()
		p.scanner.Scan()
		if p.scanner.TokenText() != ")" {
			return nil, errors.New("invalid include value! missing closing parenthesis")
		}
		token = path
	}

	if required {
		p.scanner.Scan()
		if p.scanner.TokenText() != ")" {
			return nil, errors.New("invalid include value! missing closing parenthesis")
		}
	}

	tokenLength := len(token)
	if !strings.HasPrefix(token, `"`) || !strings.HasSuffix(token, `"`) || tokenLength < 2 {
		return nil, errors.New(`invalid include value! expected quoted string, optionally wrapped in 'file(...)' or 'classpath(...)'`)
	}
	return &IncludeToken{path: token[1 : tokenLength-1], required: required}, nil // remove double quotes
}

func (p *Parser) parseIncludedResource() (includeObject *ConfigObject, err error) {
	includeToken, err := p.validateIncludeValue()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(includeToken.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !includeToken.required {
			return NewConfigObject(map[string]ConfigValue{}), nil
		}
		return nil, fmt.Errorf("could not parse resource: %w", err)
	}
	includeParser := newParser(file)
	defer func() {
		if closingErr := file.Close(); closingErr != nil {
			err = closingErr
		}
	}()

	includeParser.scanner.Scan()
	if includeParser.scanner.TokenText() == arrayStartToken {
		return nil, errors.New("invalid included file! included file cannot contain an array as the root value")
	}

	return includeParser.extractConfigObject()
}

func (p *Parser) extractConfigArray() (*ConfigArray, error) {
	var values []ConfigValue
	if firstToken := p.scanner.TokenText(); firstToken != arrayStartToken {
		return nil, invalidConfigArray(fmt.Sprintf("%q is not an array start token", firstToken), p.scanner.Position.Line, p.scanner.Position.Column)
	}
	parenthesisBalanced := false
	currentRune := p.scanner.Scan()
	if p.scanner.TokenText() == arrayEndToken { // empty array
		currentRune = p.scanner.Scan()
		return NewConfigArray(values), nil
	}
	for tok := p.scanner.Peek() ; tok != scanner.EOF; tok = p.scanner.Peek() {
		configValue, err := p.extractConfigValue(currentRune)
		if err != nil {
			return nil, err
		}
		values = append(values, configValue)
		if p.scanner.TokenText() == commaToken {
			currentRune = p.scanner.Scan() // skip comma
		}

		if !parenthesisBalanced && p.scanner.TokenText() == arrayEndToken {
			parenthesisBalanced = true
			currentRune = p.scanner.Scan()
			break
		}
	}
	if !parenthesisBalanced {
		return nil, invalidConfigArray("parenthesis do not match", p.scanner.Position.Line, p.scanner.Position.Column)
	}
	return NewConfigArray(values), nil
}

func (p *Parser) extractConfigValue(currentRune rune) (ConfigValue, error) {
	token := p.scanner.TokenText()
	switch currentRune {
	case scanner.Int:
		value, err := strconv.Atoi(token)
		if err != nil {
			return nil, err
		}
		p.scanner.Scan()
		return NewConfigInt(value), nil
	case scanner.Float:
		value, err := strconv.ParseFloat(token, 32)
		if err != nil {
			return nil, err
		}
		p.scanner.Scan()
		return NewConfigFloat32(float32(value)), nil
	case scanner.String:
		p.scanner.Scan()
		configString := NewConfigString(strings.ReplaceAll(token, `"`, ""))
		return configString, nil
	case scanner.Ident:
		if token == string(null) {
			p.scanner.Scan()
			return null, nil
		}
		if isBooleanString(token) {
			p.scanner.Scan()
			return NewConfigBooleanFromString(token), nil
		}
	default:
		switch {
		case token == objectStartToken:
			return p.extractConfigObject()
		case token == arrayStartToken:
			return p.extractConfigArray()
		case isSubstitution(token, p.scanner.Peek()):
			return p.extractSubstitution()
		}
	}
	return nil, fmt.Errorf("unknown config value: %q", token)
}

func (p *Parser) extractSubstitution() (*Substitution, error) {
	p.scanner.Scan() // skip "$"
	p.scanner.Scan() // skip "{"
	optional := false
	if p.scanner.TokenText() == "?" {
		optional = true
		p.scanner.Scan()
	}
	firstToken := p.scanner.TokenText()
	if firstToken == objectEndToken {
		return nil, invalidSubstitutionError("path expression cannot be empty", p.scanner.Position.Line, p.scanner.Position.Column)
	}
	if firstToken == dotToken {
		return nil, leadingPeriodError(p.scanner.Position.Line, p.scanner.Position.Column)
	}

	var pathBuilder strings.Builder
	parenthesisBalanced := false
	var previousToken string
	for tok := p.scanner.Peek(); tok != scanner.EOF; p.scanner.Peek() {
		pathBuilder.WriteString(p.scanner.TokenText())
		p.scanner.Scan()
		text := p.scanner.TokenText()

		if previousToken == dotToken && text == dotToken {
			return nil, adjacentPeriodsError(p.scanner.Position.Line, p.scanner.Position.Column)
		}

		if text == objectEndToken {
			if previousToken == dotToken {
				return nil, trailingPeriodError(p.scanner.Position.Line, p.scanner.Position.Column - 1)
			}
			parenthesisBalanced = true
			p.scanner.Scan()
			break
		}

		if forbiddenCharacters[text] {
			return nil, fmt.Errorf("invalid key! %q is a forbidden character in keys", text)
		}

		previousToken = text
	}

	if !parenthesisBalanced {
		return nil, invalidSubstitutionError("missing closing parenthesis", p.scanner.Position.Line, p.scanner.Position.Column)
	}

	return &Substitution{path: pathBuilder.String(), optional:optional}, nil
}

func isBooleanString(token string) bool {
	return token == "true" || token == "yes" || token == "on" || token == "false" || token == "no" || token == "off"
}

func isSubstitution(token string, peekedToken rune) bool {
	return token == "$" && peekedToken == '{'
}

func isSeparator(token string, peekedToken rune) bool {
	return token == equalsToken || token == colonToken || (token == "+" && peekedToken == '=')
}

type IncludeToken struct {
	path     string
	required bool
}