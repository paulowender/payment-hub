package hub

import (
	"bytes"
	"encoding/json"
)

type Metadata map[string]any

func decodeMetadata(raw json.RawMessage) (Metadata, error) {
	if len(raw) == 0 {
		return Metadata{}, nil
	}
	if len(raw) > 8*1024 || !json.Valid(raw) {
		return nil, errInvalid
	}
	var metadata Metadata
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&metadata) != nil || metadata == nil {
		return nil, errInvalid
	}
	return metadata, nil
}
