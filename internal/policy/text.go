package policy

// Text is the localizable management text contract. Params is always an object;
// Message remains a complete English fallback for clients without translations.
type Text struct {
	Code    string         `json:"code"`
	Params  map[string]any `json:"params"`
	Message string         `json:"message"`
}

func NewText(code, message string, params ...map[string]any) Text {
	p := map[string]any{}
	if len(params) > 0 {
		for k, v := range params[0] {
			p[k] = v
		}
	}
	return Text{Code: code, Params: p, Message: message}
}

func NewDetail(path, code, message string, params ...map[string]any) Detail {
	t := NewText(code, message, params...)
	t.Params["field"] = path
	return Detail{Path: path, Code: code, Params: t.Params, Message: message}
}
