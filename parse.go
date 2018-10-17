package param

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

var textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()

// Generic parse dispatcher. This function's signature is the interface of all
// parse functions. `key` is the entire key that is currently being parsed, such
// as "foo[bar][]". `keytail` is the portion of the string that the current
// parser is responsible for, for instance "[bar][]". `values` is the list of
// values assigned to this key, and `target` is where the resulting typed value
// should be Set() to.
func parse(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	if reflect.PtrTo(t).Implements(textUnmarshalerType) {
		return parseTextUnmarshaler(key, keytail, values, target)
	}

	switch k := target.Kind(); k {
	case reflect.Bool:
		return parseBool(key, keytail, values, target)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return parseInt(key, keytail, values, target)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return parseUint(key, keytail, values, target)
	case reflect.Float32, reflect.Float64:
		return parseFloat(key, keytail, values, target)
	case reflect.Map:
		return parseMap(key, keytail, values, target)
	case reflect.Ptr:
		return parsePtr(key, keytail, values, target)
	case reflect.Slice:
		return parseSlice(key, keytail, values, target)
	case reflect.String:
		return parseString(key, keytail, values, target)
	case reflect.Struct:
		return parseStruct(key, keytail, values, target)
	default:
		return InvalidParseError{
			Type: target.Type(),
		}
	}
}

// We pass down both the full key ("foo[bar][]") and the part the current layer
// is responsible for making sense of ("[bar][]"). This computes the other thing
// you probably want to know, which is the path you took to get here ("foo").
func kpath(key, keytail string) string {
	l, t := len(key), len(keytail)
	return key[:l-t]
}

// Helper for validating that a value has been passed exactly once, and that the
// user is not attempting to nest on the key.
func primitive(key, keytail string, tipe reflect.Type, values []string) error {
	if keytail != "" {
		return NestingError{
			Key:     kpath(key, keytail),
			Type:    tipe,
			Nesting: keytail,
		}
	}

	if len(values) != 1 {
		return SingletonError{
			Key:    kpath(key, keytail),
			Type:   tipe,
			Values: values,
		}
	}

	return nil
}

func keyed(tipe reflect.Type, key, keytail string) (string, string, error) {
	if keytail == "" || keytail[0] != '[' {
		return "", "", SyntaxError{
			Key:       kpath(key, keytail),
			Subtype:   MissingOpeningBracket,
			ErrorPart: keytail,
		}
	}

	idx := strings.IndexRune(keytail, ']')
	if idx == -1 {
		return "", "", SyntaxError{
			Key:       kpath(key, keytail),
			Subtype:   MissingClosingBracket,
			ErrorPart: keytail[1:],
		}
	}

	return keytail[1:idx], keytail[idx+1:], nil
}

func parseTextUnmarshaler(key, keytail string, values []string, target reflect.Value) error {
	err := primitive(key, keytail, target.Type(), values)
	if err != nil {
		return err
	}

	tu := target.Addr().Interface().(encoding.TextUnmarshaler)
	err = tu.UnmarshalText([]byte(values[0]))
	if err != nil {
		return ValueError{
			Key:  kpath(key, keytail),
			Type: target.Type(),
			Err:  err,
		}
	}

	return nil
}

func parseBool(key, keytail string, values []string, target reflect.Value) error {
	err := primitive(key, keytail, target.Type(), values)
	if err != nil {
		return err
	}

	switch values[0] {
	case "true", "1", "on":
		target.SetBool(true)
		return nil
	case "false", "0", "":
		target.SetBool(false)
		return nil
	default:
		return ValueError{
			Key:  kpath(key, keytail),
			Type: target.Type(),
		}
	}
}

func parseInt(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	err := primitive(key, keytail, t, values)
	if err != nil {
		return err
	}

	i, err := strconv.ParseInt(values[0], 10, t.Bits())
	if err != nil {
		return ValueError{
			Key:  kpath(key, keytail),
			Type: t,
			Err:  err.(*strconv.NumError).Err,
		}
	}

	target.SetInt(i)
	return nil
}

func parseUint(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	err := primitive(key, keytail, t, values)
	if err != nil {
		return err
	}

	i, err := strconv.ParseUint(values[0], 10, t.Bits())
	if err != nil {
		return ValueError{
			Key:  kpath(key, keytail),
			Type: t,
			Err:  err.(*strconv.NumError).Err,
		}
	}

	target.SetUint(i)
	return nil
}

func parseFloat(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	err := primitive(key, keytail, t, values)
	if err != nil {
		return err
	}

	f, err := strconv.ParseFloat(values[0], t.Bits())
	if err != nil {
		return ValueError{
			Key:  kpath(key, keytail),
			Type: t,
			Err:  err.(*strconv.NumError).Err,
		}
	}

	target.SetFloat(f)
	return nil
}

func parseString(key, keytail string, values []string, target reflect.Value) error {
	err := primitive(key, keytail, target.Type(), values)
	if err != nil {
		return err
	}

	target.SetString(values[0])
	return nil
}

func parseSlice(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()

	// BUG(carl): We currently do not handle slices of nested types. If
	// support is needed, the implementation probably could be fleshed out.
	if keytail != "[]" {
		return NestingError{
			Key:     kpath(key, keytail),
			Type:    t,
			Nesting: keytail,
		}
	}

	slice := reflect.MakeSlice(t, len(values), len(values))
	kp := kpath(key, keytail)
	for i := range values {
		// We actually cheat a little bit and modify the key so we can
		// generate better debugging messages later
		key := fmt.Sprintf("%s[%d]", kp, i)
		err := parse(key, "", values[i:i+1], slice.Index(i))
		if err != nil {
			return err
		}
	}

	target.Set(slice)
	return nil
}

func parseMap(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	mapkey, maptail, err := keyed(t, key, keytail)
	if err != nil {
		return err
	}

	// BUG(carl): We don't support any map keys except strings, although
	// there's no reason we shouldn't be able to throw the value through our
	// unparsing stack.
	var mk reflect.Value
	if t.Key().Kind() == reflect.String {
		mk = reflect.ValueOf(mapkey).Convert(t.Key())
	} else {
		return InvalidParseError{
			Type: t,
			Hint: fmt.Sprintf("map keys must be strings, not %v", t.Key()),
		}
	}

	if target.IsNil() {
		target.Set(reflect.MakeMap(t))
	}

	val := target.MapIndex(mk)
	if !val.IsValid() || !val.CanSet() {
		// It's a teensy bit annoying that the value returned by
		// MapIndex isn't Set()table if the key exists.
		val = reflect.New(t.Elem()).Elem()
	}

	err = parse(key, maptail, values, val)
	if err != nil {
		return err
	}

	target.SetMapIndex(mk, val)
	return nil
}

func parseStruct(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	sk, skt, err := keyed(t, key, keytail)
	if err != nil {
		return err
	}

	cache, err := cacheStruct(t)
	if err != nil {
		return err
	}

	return parseStructField(cache, key, sk, skt, values, target)
}

func parsePtr(key, keytail string, values []string, target reflect.Value) error {
	t := target.Type()
	if target.IsNil() {
		target.Set(reflect.New(t.Elem()))
	}

	return parse(key, keytail, values, target.Elem())
}
