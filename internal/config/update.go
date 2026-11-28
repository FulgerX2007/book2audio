package config

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// UpdateYAML sets values in a YAML config and returns the new text. Keys
// are dotted paths such as "translate.model"; a value is a string, a list of
// strings or a map of strings. Everything else in the
// document, comments included, is kept. The result is parsed and validated
// as a config: an update that makes the config invalid is an error.
func UpdateYAML(data []byte, set map[string]any) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if doc.Kind == 0 {
		// An empty file: start a document with an empty mapping.
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config: the top level must be a mapping")
	}
	for key, value := range set {
		if err := setPath(root, strings.Split(key, "."), value); err != nil {
			return nil, fmt.Errorf("config: %s: %w", key, err)
		}
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if _, err := Parse(bytes.NewReader(out.Bytes())); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// setPath sets the value at path inside the mapping node, creating the
// mappings on the way.
func setPath(node *yaml.Node, path []string, value any) error {
	for i, key := range path {
		var child *yaml.Node
		for j := 0; j+1 < len(node.Content); j += 2 {
			if node.Content[j].Value == key {
				child = node.Content[j+1]
				break
			}
		}
		last := i == len(path)-1
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			if last {
				child = &yaml.Node{}
			}
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
		}
		if last {
			// Keep the comments of the line; replace only the value.
			switch v := value.(type) {
			case string:
				if child.Kind != 0 && child.Kind != yaml.ScalarNode {
					return fmt.Errorf("is not a simple value")
				}
				child.Kind, child.Tag, child.Value, child.Style, child.Content = yaml.ScalarNode, "!!str", v, 0, nil
				if v == "" {
					child.Style = yaml.DoubleQuotedStyle
				}
			case []string:
				if child.Kind == yaml.MappingNode {
					return fmt.Errorf("is not a list")
				}
				child.Kind, child.Tag, child.Value, child.Style = yaml.SequenceNode, "!!seq", "", yaml.FlowStyle
				child.Content = nil
				for _, item := range v {
					child.Content = append(child.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item, Style: yaml.DoubleQuotedStyle})
				}
			case map[string]string:
				if child.Kind == yaml.SequenceNode || (child.Kind == yaml.ScalarNode && child.Value != "") {
					return fmt.Errorf("is not a section")
				}
				child.Kind, child.Tag, child.Value, child.Style = yaml.MappingNode, "!!map", "", 0
				child.Content = nil
				names := make([]string, 0, len(v))
				for name := range v {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					child.Content = append(child.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v[name], Style: yaml.DoubleQuotedStyle})
				}
				if len(v) == 0 {
					child.Style = yaml.FlowStyle // "{}"
				}
			default:
				return fmt.Errorf("unsupported value %T", value)
			}
			return nil
		}
		if child.Kind == yaml.ScalarNode && child.Value == "" {
			// "translate:" with nothing under it.
			child.Kind, child.Tag, child.Style = yaml.MappingNode, "!!map", 0
		}
		if child.Kind != yaml.MappingNode {
			return fmt.Errorf("%s is not a section", strings.Join(path[:i+1], "."))
		}
		node = child
	}
	return nil
}
