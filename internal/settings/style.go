package settings

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type StyleRule struct {
	Masks []string
	Color string
}

// StyleConfig is immutable after loading and safe to share between local and
// remote directory services.
type StyleConfig struct {
	rules []StyleRule
}

type styleFile struct {
	Style []styleRule `yaml:"style"`
}

type styleRule struct {
	Mask  []string `yaml:"mask"`
	Color string   `yaml:"color"`
}

var styleColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// LoadStyleConfig reads one strict YAML document. A missing file and an empty
// rule list both mean the default application appearance.
func LoadStyleConfig(filename string) (StyleConfig, error) {
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return StyleConfig{}, nil
	}
	if err != nil {
		return StyleConfig{}, fmt.Errorf("открыть style.yaml: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var parsed styleFile
	if err := decoder.Decode(&parsed); errors.Is(err, io.EOF) {
		return StyleConfig{}, nil
	} else if err != nil {
		return StyleConfig{}, fmt.Errorf("прочитать style.yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return StyleConfig{}, errors.New("style.yaml содержит несколько YAML-документов")
		}
		return StyleConfig{}, fmt.Errorf("прочитать style.yaml: %w", err)
	}

	config := StyleConfig{rules: make([]StyleRule, 0, len(parsed.Style))}
	for ruleIndex, value := range parsed.Style {
		if len(value.Mask) == 0 {
			return StyleConfig{}, fmt.Errorf("style[%d]: укажите хотя бы одну mask", ruleIndex)
		}
		if !styleColor.MatchString(value.Color) {
			return StyleConfig{}, fmt.Errorf("style[%d]: цвет %q должен иметь формат #RRGGBB", ruleIndex, value.Color)
		}
		rule := StyleRule{Masks: make([]string, 0, len(value.Mask)), Color: strings.ToUpper(value.Color)}
		for maskIndex, mask := range value.Mask {
			// Accept the spelling from the documented YAML example as well as
			// ordinary glob stars. YAML single quotes do not consume backslashes.
			mask = strings.ReplaceAll(strings.TrimSpace(mask), `\*`, `*`)
			mask = strings.ReplaceAll(mask, `\?`, `?`)
			if mask == "" || strings.ContainsRune(mask, 0) || strings.Contains(mask, "/") {
				return StyleConfig{}, fmt.Errorf("style[%d].mask[%d]: маска должна описывать только имя файла", ruleIndex, maskIndex)
			}
			mask = strings.ToLower(mask)
			if _, err := path.Match(mask, ""); err != nil {
				return StyleConfig{}, fmt.Errorf("style[%d].mask[%d] %q: некорректная glob-маска: %w", ruleIndex, maskIndex, mask, err)
			}
			rule.Masks = append(rule.Masks, mask)
		}
		config.rules = append(config.rules, rule)
	}
	return config, nil
}

// Color returns the first matching rule's colour. Matching is against the
// complete provider-supplied basename and is case-insensitive.
func (c StyleConfig) Color(name string) (string, bool) {
	name = strings.ToLower(name)
	for _, rule := range c.rules {
		for _, mask := range rule.Masks {
			if matched, _ := path.Match(mask, name); matched {
				return rule.Color, true
			}
		}
	}
	return "", false
}
