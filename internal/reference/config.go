package reference

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// ConfigKey is one leaf key of funcdconfig.yaml.
type ConfigKey struct {
	Path, Type, Default, Limits, Env, Description string
}

// ConfigKeys lists every leaf key of config.Config in declaration order, its default taken from defaults.
func ConfigKeys(defaults config.Config) []ConfigKey {
	return keysOf(reflect.ValueOf(defaults), "")
}

// keysOf lists the leaf keys of the struct v under prefix. A list of structs contributes its element's keys as
// path[].key, with no default.
func keysOf(v reflect.Value, prefix string) []ConfigKey {
	var keys []ConfigKey
	for i := range v.NumField() {
		field := v.Type().Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		switch t := field.Type; {
		case t.Kind() == reflect.Struct:
			keys = append(keys, keysOf(v.Field(i), path)...)
		case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Struct:
			keys = append(keys, keysOf(reflect.New(t.Elem()).Elem(), path+"[]")...)
		default:
			keys = append(keys, ConfigKey{
				Path:        path,
				Type:        typeName(t),
				Default:     defaultOf(v.Field(i)),
				Limits:      limitsOf(field.Tag.Get("validate")),
				Env:         envOf(field),
				Description: field.Tag.Get("doc"),
			})
		}
	}
	return keys
}

func typeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Slice:
		return "list of " + typeName(t.Elem()) + "s"
	case reflect.Map:
		return "map of " + typeName(t.Key()) + " to " + typeName(t.Elem())
	default:
		return t.Kind().String()
	}
}

// defaultOf writes a default as funcdconfig.yaml would hold it; an empty string, list or map has none.
func defaultOf(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Slice:
		items := make([]string, v.Len())
		for i := range items {
			items[i] = fmt.Sprint(v.Index(i).Interface())
		}
		return strings.Join(items, ", ")
	case reflect.Map:
		pairs := make([]string, 0, v.Len())
		for _, k := range v.MapKeys() {
			pairs = append(pairs, fmt.Sprintf("%v=%v", k.Interface(), v.MapIndex(k).Interface()))
		}
		slices.Sort(pairs)
		return strings.Join(pairs, ", ")
	default:
		return fmt.Sprint(v.Interface())
	}
}

// limitsOf maps the validate rules that a reader needs (ADR-0324 Decision 2). Any other limit is written in the
// key's doc tag.
func limitsOf(rules string) string {
	var limits []string
	var low, high string
	for _, rule := range strings.Split(rules, ",") {
		name, arg, _ := strings.Cut(rule, "=")
		switch name {
		case "oneof":
			values := strings.Fields(arg)
			for i, value := range values {
				values[i] = "`" + value + "`"
			}
			limits = append(limits, "one of "+join(values))
		case "min":
			low = arg
		case "max":
			high = arg
		case "required":
			limits = append(limits, "required")
		case "eq":
			limits = append(limits, "must be `"+arg+"`")
		}
	}
	switch {
	case low != "" && high != "":
		limits = append(limits, low+" to "+high)
	case low != "":
		limits = append(limits, "at least "+low)
	case high != "":
		limits = append(limits, "at most "+high)
	}
	return strings.Join(limits, "; ")
}

// envOf names the key's variable, with the form that a list or a map takes in it.
func envOf(field reflect.StructField) string {
	name := field.Tag.Get("env")
	if name == "" || name == "-" {
		return ""
	}
	separator := field.Tag.Get("envSeparator")
	if separator == "" {
		separator = ","
	}
	switch field.Type.Kind() {
	case reflect.Slice:
		return name + "=a" + separator + "b"
	case reflect.Map:
		pair := field.Tag.Get("envKeyValSeparator")
		if pair == "" {
			pair = ":"
		}
		return name + "=k1" + pair + "v1" + separator + "k2" + pair + "v2"
	default:
		return name
	}
}

// ConfigTable renders keys as one Markdown table, laid out as the kinds' Spec pages are (ADR-0323).
func ConfigTable(keys []ConfigKey) []byte {
	var b bytes.Buffer
	b.WriteString("| Key | Type and limits | Default | Description |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, k := range keys {
		kind := k.Type
		if k.Limits != "" {
			kind += ", " + k.Limits
		}
		description := cell(k.Description)
		if k.Env != "" {
			description = strings.TrimSpace(description + " Variable: " + code(k.Env) + ".")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", code(k.Path), cell(kind), code(k.Default), description)
	}
	return b.Bytes()
}

func join(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}
