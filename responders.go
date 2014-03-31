// The web_responders package takes care of our custom vendor codecs for
// Radiobox, handling responses, and even providing helpers for parsing
// input parameters.
package web_responders

import (
	"errors"
	"fmt"
	"github.com/Radiobox/web_request_readers"
	"github.com/stretchr/goweb"
	"github.com/stretchr/goweb/context"
	"github.com/stretchr/objx"
	"net/http"
	"reflect"
	"strings"
	"unicode"
)

// database/sql has nullable values which all have the same prefix.
const SqlNullablePrefix = "Null"

// CreateResponse takes a value to be used as a response and attempts
// to generate a value to respond with, based on struct tag and
// interface matching.
//
// Values which implement LazyLoader will have their LazyLoad method
// run first, in order to load any values that haven't been loaded
// yet.
//
// Struct values will be converted to a map[string]interface{}.  Each
// field will be assigned a key - the "request" tag's value if it
// exists, or the "response" tag's value if it exists, or just the
// lowercase field name if neither tag exists.  A value of "-" for the
// key (i.e. the value of a request or response tag) will result in
// the field being skipped.
//
// CreateResponse will skip parsing any sub-elements of a response
// (i.e. entries in a slice or map, or fields of a struct) that
// implement the ResponseValueCreator, and instead just use the return
// value of their ResponseValue() method.
func CreateResponse(data interface{}, optionList ...interface{}) interface{} {
	if err, ok := data.(error); ok {
		return err.Error()
	}

	// Parse options
	var (
		options     objx.Map
		constructor func(interface{}, interface{}) interface{}
	)
	switch len(optionList) {
	case 2:
		constructor = optionList[1].(func(interface{}, interface{}) interface{})
		fallthrough
	case 1:
		options = optionList[0].(objx.Map)
	}
	return createResponse(data, false, options, constructor)
}

func createResponse(data interface{}, isSubResponse bool, options objx.Map, constructor func(interface{}, interface{}) interface{}) interface{} {

	// LazyLoad with options
	if lazyLoader, ok := data.(LazyLoader); ok {
		lazyLoader.LazyLoad(options)
	}

	responseData := data
	if responseCreator, ok := data.(ResponseObjectCreator); ok {
		responseData = responseCreator.ResponseObject()
	}

	value := reflect.ValueOf(responseData)
	if value.Kind() == reflect.Ptr {
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Struct:
		data = createStructResponse(value, options, constructor)
	case reflect.Slice, reflect.Array:
		data = createSliceResponse(value, options, constructor)
		if options != nil && isSubResponse {
			data = constructor(data, value)
		}
	case reflect.Map:
		data = createMapResponse(value, options, constructor)
	default:
		data = responseData
	}
	return data
}

// createNullableDbResponse checks for "database/sql".Null* types, or
// anything with a similar structure, and pulls out the underlying
// value.  For example:
//
//     type NullInt struct {
//         Int int
//         Valid bool
//     }
//
// If Valid is false, this function will return nil; otherwise, it
// will return the value of the Int field.
func createNullableDbResponse(value reflect.Value, valueType reflect.Type) (interface{}, error) {
	typeName := valueType.Name()
	if strings.HasPrefix(typeName, SqlNullablePrefix) {
		fieldName := typeName[len(SqlNullablePrefix):]
		val := value.FieldByName(fieldName)
		isNotNil := value.FieldByName("Valid")
		if val.IsValid() && isNotNil.IsValid() {
			// We've found a nullable type
			if isNotNil.Interface().(bool) {
				return val.Interface(), nil
			} else {
				return nil, nil
			}
		}
	}
	return nil, errors.New("No Nullable DB value found")
}

// createMapResponse is a helper for generating a response value from
// a value of type map.
func createMapResponse(value reflect.Value, options objx.Map, constructor func(interface{}, interface{}) interface{}) interface{} {
	response := reflect.MakeMap(value.Type())
	for _, key := range value.MapKeys() {
		var elementOptions objx.Map
		keyStr := key.Interface().(string)
		if options != nil {
			var elementOptionsValue *objx.Value
			if options.Has(keyStr) {
				elementOptionsValue = options.Get(keyStr)
			} else if options.Has("*") {
				elementOptionsValue = options.Get("*")
			}
			if elementOptionsValue.IsMSI() {
				elementOptions = objx.Map(elementOptionsValue.MSI())
			} else if elementOptionsValue.IsObjxMap() {
				elementOptions = elementOptionsValue.ObjxMap()
			} else {
				panic("Don't know what to do with option")
			}
		}
		itemResponse := createResponseValue(value.MapIndex(key), elementOptions, constructor)
		response.SetMapIndex(key, reflect.ValueOf(itemResponse))
	}
	return response.Interface()
}

// createSliceResponse is a helper for generating a response value
// from a value of type slice.
func createSliceResponse(value reflect.Value, options objx.Map, constructor func(interface{}, interface{}) interface{}) interface{} {
	response := make([]interface{}, 0, value.Len())
	for i := 0; i < value.Len(); i++ {
		element := value.Index(i)
		response = append(response, createResponseValue(element, options, constructor))
	}
	return response
}

func ResponseTag(field reflect.StructField) string {
	var name string
	if name = field.Tag.Get("response"); name != "" {
		return name
	}
	if field.Name != "Id" {
		if name = field.Tag.Get("db"); name != "" && name != "-" {
			return name
		}
	}
	return strings.ToLower(field.Name)
}

// createStructResponse is a helper for generating a response value
// from a value of type struct.
func createStructResponse(value reflect.Value, options objx.Map, constructor func(interface{}, interface{}) interface{}) interface{} {
	structType := value.Type()

	// Support "database/sql".Null* types, and any other types
	// matching that structure
	if v, err := createNullableDbResponse(value, structType); err == nil {
		return v
	}

	response := make(objx.Map)

	for i := 0; i < value.NumField(); i++ {
		fieldType := structType.Field(i)
		fieldValue := value.Field(i)

		if fieldType.Anonymous {
			embeddedResponse := CreateResponse(fieldValue.Interface(), options, constructor).(objx.Map)
			for key, value := range embeddedResponse {
				// Don't overwrite values from the base struct
				if _, ok := response[key]; !ok {
					response[key] = value
				}
			}
		} else if unicode.IsUpper(rune(fieldType.Name[0])) {
			name := ResponseTag(fieldType)
			switch name {
			case "-":
				continue
			default:
				var subOptions objx.Map
				if options != nil && (options.Has(name) || options.Has("*")) {
					var subOptionsValue *objx.Value
					if options.Has(name) {
						subOptionsValue = options.Get(name)
					} else {
						subOptionsValue = options.Get("*")
					}
					if subOptionsValue.IsMSI() {
						subOptions = objx.Map(subOptionsValue.MSI())
					} else if subOptionsValue.IsObjxMap() {
						subOptions = subOptionsValue.ObjxMap()
					} else {
						panic("Don't know what to do with option")
					}
				}
				response[name] = createResponseValue(fieldValue, subOptions, constructor)
			}
		}
	}
	return response
}

// createResponseValue is a helper for generating a response value for
// a single value in a response object.
func createResponseValue(value reflect.Value, options objx.Map, constructor func(interface{}, interface{}) interface{}) (responseValue interface{}) {
	if options.Get("type").Str() != "full" {
		switch source := value.Interface().(type) {
		case ResponseValueCreator:
			responseValue = source.ResponseValue(options)
		case fmt.Stringer:
			responseValue = source.String()
		case error:
			responseValue = source.Error()
		default:
			responseValue = createResponse(value.Interface(), true, options, constructor)
		}
	} else {
		responseValue = createResponse(value.Interface(), true, options, constructor)
	}
	return
}

// RespondWithInputErrors attempts to figure out where the input
// values (in ctx) may have caused problems when being set to fields
// on data, and then add them to the input errors on the notifications
// map.
//
// For each field in data, if the field is an InputValidator,
// the input checking logic will just be handed off to its
// ValidateInput method; if the field is a RequestValueReceiver, the
// error value returned from Receive will be used to validate;
// otherwise, we will attempt to check that the input value is
// assignable to the field.
func RespondWithInputErrors(ctx context.Context, notifications MessageMap, data interface{}) error {
	dataType := reflect.TypeOf(data)
	params, err := web_request_readers.ParseParams(ctx)
	if err != nil {
		return err
	}
	addInputErrors(dataType, params, notifications)

	for key := range params {
		notifications.SetInputMessage(key, "No target field found for this input")
	}
	return Respond(ctx, http.StatusBadRequest, notifications, notifications)
}

// addInputErrors (which, to be honest, should be in the
// web_request_parsers package) walks through
func addInputErrors(dataType reflect.Type, params objx.Map, notifications MessageMap) {
	for i := 0; i < dataType.NumField(); i++ {
		field := dataType.Field(i)
		if field.Anonymous {
			addInputErrors(field.Type, params, notifications)
			continue
		}

		name, args := web_request_readers.NameAndArgs(dataType.Field(i))

		optional := false
		for _, arg := range args {
			if arg == "optional" {
				optional = true
			}
		}

		value, ok := params[name]
		if !ok {
			if !optional {
				notifications.SetInputMessage(name, "No input for required field")
			}
			continue
		}

		// We're now at the point where we know this parameter has a
		// target field and will be checked, so remove it from the
		// map.
		delete(params, name)

		var emptyValue reflect.Value
		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			emptyValue = reflect.New(fieldType.Elem())
		} else {
			emptyValue = reflect.Zero(fieldType)
		}

		// A type switch would look cleaner here, but we want a very
		// specific order of preference for these interfaces.  A type
		// switch does not guarantee any preferred order, just that
		// one valid case will be executed.
		emptyInter := emptyValue.Interface()
		if validator, ok := emptyInter.(InputValidator); ok {
			if err := validator.ValidateInput(value); err != nil {
				notifications.SetInputMessage(name, err.Error())
			}
			continue
		}
		if receiver, ok := emptyInter.(web_request_readers.RequestValueReceiver); ok {
			if err := receiver.Receive(value); err != nil {
				notifications.SetInputMessage(name, err.Error())
			}
			continue
		}
		if !reflect.TypeOf(value).ConvertibleTo(fieldType) {
			notifications.SetInputMessage(name, "Input is of the wrong type and cannot be converted")
		}
	}
}

// Respond performs an API response, adding some additional data to
// the context's CodecOptions to support our custom codecs.  This
// particular function is very specifically for use with the
// github.com/stretchr/goweb web framework.
//
// TODO: Move the with={} parameter to options in the mimetypes in the
// Accept header.
func Respond(ctx context.Context, status int, notifications MessageMap, data interface{}) error {
	params, err := web_request_readers.ParseParams(ctx)
	if err != nil {
		return err
	}
	if ctx.QueryParams().Has("joins") {
		params.Set("joins", ctx.QueryValue("joins"))
	}

	protocol := "http"
	if ctx.HttpRequest().TLS != nil {
		protocol += "s"
	}

	host := ctx.HttpRequest().Host

	if linker, ok := data.(RelatedLinker); ok {
		linkMap := linker.RelatedLinks()
		links := make([]string, 0, len(linkMap))
		for rel, link := range linkMap {
			link := fmt.Sprintf(`<%s://%s%s>; rel="%s"`, protocol, host, link, rel)
			links = append(links, link)
		}
		ctx.HttpResponseWriter().Header().Set("Link", strings.Join(links, ", "))
	}

	options := ctx.CodecOptions()
	options.MergeHere(objx.Map{
		"status":        status,
		"input_params":  params,
		"notifications": notifications,
		"protocol":      protocol,
		"host":          host,
	})

	// Right now, this line is commented out to support our joins
	// logic.  Unfortunately, that means that codecs other than our
	// custom codecs from this package will not work.  Whoops.
	// data = CreateResponse(data)

	return goweb.API.WriteResponseObject(ctx, status, data)
}
