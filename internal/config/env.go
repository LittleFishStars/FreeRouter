package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ApplyEnv 用环境变量覆盖配置。
//
// 命名规则：`FR_` + YAML 路径大写、`.` 换成 `_`。
// 例：`server.listen` → `FR_SERVER_LISTEN`；`upstream.relay.allowlist` → `FR_UPSTREAM_RELAY_ALLOWLIST`。
//
// 为什么用反射而不是手写几十行 switch：手写会在新增配置项时漏掉一条，
// 而「漏掉」的表现是环境变量静默失效——这是最难排查的一类部署问题。
func (c *Config) ApplyEnv(lookup func(string) (string, bool)) error {
	return applyEnvValue(reflect.ValueOf(c).Elem(), nil, lookup)
}

func applyEnvValue(v reflect.Value, prefix []string, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name := yamlKey(field)
		if name == "" || name == "-" {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		fv := v.Field(i)

		// 嵌套结构体继续下钻；Duration 的 Kind 是整数，不会误入这一支。
		if fv.Kind() == reflect.Struct {
			if err := applyEnvValue(fv, path, lookup); err != nil {
				return err
			}
			continue
		}

		env := envName(path)
		raw, ok := lookup(env)
		if !ok {
			continue
		}
		if err := setFromString(fv, raw); err != nil {
			return fmt.Errorf("环境变量 %s: %w", env, err)
		}
	}
	return nil
}

func yamlKey(field reflect.StructField) string {
	tag := field.Tag.Get("yaml")
	if tag == "" {
		return strings.ToLower(field.Name)
	}
	return strings.Split(tag, ",")[0]
}

// envName 把路径变成环境变量名。
func envName(path []string) string {
	return "FR_" + strings.ToUpper(strings.Join(path, "_"))
}

// EnvNameFor 暴露路径 → 环境变量名的映射，供文档与 --print-config 使用。
// 例：EnvNameFor("server", "listen") == "FR_SERVER_LISTEN"。
func EnvNameFor(path ...string) string { return envName(path) }

func setFromString(v reflect.Value, raw string) error {
	raw = strings.TrimSpace(raw)

	switch v.Type() {
	case reflect.TypeOf(Duration(0)):
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("非法 duration %q", raw)
		}
		v.SetInt(int64(d))
		return nil
	case reflect.TypeOf([]string{}):
		if raw == "" {
			v.Set(reflect.ValueOf([]string{}))
			return nil
		}
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		v.Set(reflect.ValueOf(out))
		return nil
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("非法布尔值 %q（接受 true/false/1/0）", raw)
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("非法整数 %q", raw)
		}
		v.SetInt(n)
	default:
		return fmt.Errorf("不支持的配置类型 %s", v.Type())
	}
	return nil
}
