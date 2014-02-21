// The codec package defines the codec that is used to ensure certain
// format restrictions when creating responses from our API.  We have
// a few formats that create different types of metadata in the
// response.  Right now, because of restrictions within the
// stretchr/goweb and stretchr/codecs package, our codec package only
// supports json, and it only checks to make sure that the response is
// formatted properly before returning.  It doesn't do any formatting
// itself, just yet.
package codecs

import (
	"errors"
	"fmt"
	"github.com/Radiobox/web_responders"
	"github.com/stretchr/goweb"
	"github.com/stretchr/objx"
	"log"
	"path"
	"strings"
)

const (
	typeCategory    = "application"
	typeName        = "vnd.radiobox.encapsulated"
	BasicMimeType   = typeCategory + "/" + typeName
	defaultBaseType = "application/json"
)

type RadioboxApiCodec struct {
}

func (codec *RadioboxApiCodec) CreateConstructor(options map[string]interface{}) func(interface{}, interface{}) interface{} {
	return func(object interface{}, originalObject interface{}) interface{} {
		var links map[string]string
		if linker, ok := originalObject.(web_responders.RelatedLinker); ok {
			links = linker.RelatedLinks()
		} else {
			links = map[string]string{}
		}
		protocol := options["protocol"].(string)
		host := options["host"].(string)
		for rel, link := range links {
			fullLink := path.Join(host, link)
			links[rel] = fmt.Sprintf("%s://%s", protocol, fullLink)
		}

		response := map[string]interface{}{
			"meta": map[string]interface{}{
				"code":         options["status"],
				"input_params": options["input_params"],
				"links":        links,
			},
			"notifications": options["notifications"],
			"response":      object,
		}
		return response
	}
}

// Marshal encapsulates the passed in object with our encapsulation
// format.
func (codec *RadioboxApiCodec) Marshal(object interface{}, options map[string]interface{}) ([]byte, error) {
	var joinsStr string
	if joinsValue, ok := options["joins"]; ok {
		joinsStr = joinsValue.(string)
	} else {
		joinsStr = options["input_params"].(objx.Map).Get("joins").Str()
	}
	var joins objx.Map
	if joinsStr != "" {
		var err error
		joins, err = objx.FromJSON(joinsStr)
		if err != nil {
			log.Print("Could not load joins options: " + err.Error())
		}
	}
	constructor := codec.CreateConstructor(options)
	responseObject := web_responders.CreateResponse(object, joins, constructor)
	response := constructor(responseObject, object)

	matchedType, ok := options["matched_type"].(string)
	var baseType string
	if ok && strings.ContainsRune(matchedType, '+') {
		baseType = typeCategory + "/" + matchedType[len(codec.ContentType())+1:]
	} else {
		baseType = defaultBaseType
	}
	baseCodec, err := goweb.CodecService.GetCodec(baseType)
	if err != nil {
		return nil, err
	}

	return baseCodec.Marshal(response, options)
}

// Unmarshal returns an error, because unmarshaling is currently
// unsupported with this codec.
func (codec *RadioboxApiCodec) Unmarshal(data []byte, obj interface{}) error {
	return errors.New("Unmarshal not supported")
}

func (codec *RadioboxApiCodec) ContentType() string {
	return BasicMimeType
}

// ContentTypeSupported checks a mime type string to see if this codec
// can support responses in that format.
func (codec *RadioboxApiCodec) ContentTypeSupported(contentType string) bool {
	if index := strings.IndexRune(contentType, '+'); index != -1 {
		contentType = contentType[:index]
	}
	return contentType == codec.ContentType()
}

func (codec *RadioboxApiCodec) FileExtension() string {
	return ".rbx"
}

func (codec *RadioboxApiCodec) CanMarshalWithCallback() bool {
	return true
}

func AddCodecs() {
	goweb.CodecService.AddCodec(new(RadioboxApiCodec))
}
