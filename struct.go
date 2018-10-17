package param

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// We decode a lot of structs (since it's the top-level thing this library
// decodes) and it takes a fair bit of work to reflect upon the struct to figure
// out what we want to do. Instead of doing this on every invocation, we cache
// metadata about each struct the first time we see it. The upshot is that we
// save some work every time. The downside is we are forced to briefly acquire
// a lock to access the cache in a thread-safe way. If this ever becomes a
// bottleneck, both the lock and the cache can be sharded or something.
type structCache map[string]cacheLine
type cacheLine struct {
	offset int
	parse  func(string, string, []string, reflect.Value) error
}

var cacheLock sync.RWMutex
var cache = make(map[reflect.Type]structCache)

func cacheStruct(t reflect.Type) (structCache, error) {
	cacheLock.RLock()
	sc, ok := cache[t]
	cacheLock.RUnlock()

	if ok {
		return sc, nil
	}

	// It's okay if two people build struct caches simultaneously
	sc = make(structCache)
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		// Only unexported fields have a PkgPath; we want to only cache
		// exported fields.
		if sf.PkgPath != "" && !sf.Anonymous {
			continue
		}
		name := extractName(sf)
		if name != "-" {
			h, err := extractHandler(t, sf)
			if err != nil {
				return nil, err
			}

			sc[name] = cacheLine{i, h}
		}
	}

	cacheLock.Lock()
	cache[t] = sc
	cacheLock.Unlock()

	return sc, nil
}

// Extract the name of the given struct field, looking at struct tags as
// appropriate.
func extractName(sf reflect.StructField) string {
	name := sf.Tag.Get("param")
	if name == "" {
		name = sf.Tag.Get("json")
		idx := strings.IndexRune(name, ',')
		if idx >= 0 {
			name = name[:idx]
		}
	}
	if name == "" {
		name = sf.Name
	}

	return name
}

func extractHandler(s reflect.Type, sf reflect.StructField) (func(string, string, []string, reflect.Value) error, error) {
	if reflect.PtrTo(sf.Type).Implements(textUnmarshalerType) {
		return parseTextUnmarshaler, nil
	}

	switch sf.Type.Kind() {
	case reflect.Bool:
		return parseBool, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return parseInt, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return parseUint, nil
	case reflect.Float32, reflect.Float64:
		return parseFloat, nil
	case reflect.Map:
		return parseMap, nil
	case reflect.Ptr:
		return parsePtr, nil
	case reflect.Slice:
		return parseSlice, nil
	case reflect.String:
		return parseString, nil
	case reflect.Struct:
		return parseStruct, nil
	default:
		return nil, InvalidParseError{
			Type: s,
			Hint: fmt.Sprintf("field %q in struct %v", sf.Name, s),
		}
	}
}

// We have to parse two types of structs: ones at the top level, whose keys
// don't have square brackets around them, and nested structs, which do.
func parseStructField(cache structCache, key, sk, keytail string, values []string, target reflect.Value) error {
	l, ok := cache[sk]
	if !ok {
		return KeyError{
			FullKey: key,
			Key:     kpath(key, keytail),
			Type:    target.Type(),
			Field:   sk,
		}
	}

	f := target.Field(l.offset)
	return l.parse(key, keytail, values, f)
}
