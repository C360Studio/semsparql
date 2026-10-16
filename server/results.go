package server

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/c360studio/semsparql/sparql"
)

// WriteSelectAskResult serializes a SELECT or ASK query Result in the negotiated
// SPARQL 1.1 result format (JSON, XML, CSV, or TSV).
func WriteSelectAskResult(w io.Writer, res *sparql.Result, format ResultFormat) error {
	if res == nil {
		return fmt.Errorf("server: nil query result")
	}
	switch format {
	case FormatSPARQLJSON:
		return writeSPARQLJSON(w, res)
	case FormatSPARQLXML:
		return writeSPARQLXML(w, res)
	case FormatCSV:
		return writeSPARQLCSV(w, res)
	case FormatTSV:
		return writeSPARQLTSV(w, res)
	default:
		return fmt.Errorf("server: unsupported SELECT/ASK result format %v", format)
	}
}

type jsonResultTerm struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Datatype string `json:"datatype,omitempty"`
	XMLLang  string `json:"xml:lang,omitempty"`
}

type jsonSelectPayload struct {
	Head struct {
		Vars []string `json:"vars"`
	} `json:"head"`
	Results struct {
		Bindings []map[string]jsonResultTerm `json:"bindings"`
	} `json:"results"`
}

type jsonAskPayload struct {
	Head    struct{} `json:"head"`
	Boolean bool     `json:"boolean"`
}

func writeSPARQLJSON(w io.Writer, res *sparql.Result) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	if res.Form == sparql.QueryAsk {
		return enc.Encode(jsonAskPayload{Boolean: res.AskResult})
	}

	var payload jsonSelectPayload
	if res.Variables == nil {
		payload.Head.Vars = []string{}
	} else {
		payload.Head.Vars = res.Variables
	}
	payload.Results.Bindings = make([]map[string]jsonResultTerm, 0, len(res.Solutions))
	for _, sol := range res.Solutions {
		row := make(map[string]jsonResultTerm, len(sol))
		for _, v := range payload.Head.Vars {
			term, ok := sol[v]
			if !ok {
				continue
			}
			row[v] = toJSONResultTerm(term)
		}
		payload.Results.Bindings = append(payload.Results.Bindings, row)
	}
	return enc.Encode(payload)
}

func toJSONResultTerm(t sparql.Term) jsonResultTerm {
	switch t.Kind {
	case sparql.TermIRI:
		return jsonResultTerm{
			Type:  "uri",
			Value: t.Value,
		}
	case sparql.TermBlankNode:
		return jsonResultTerm{
			Type:  "bnode",
			Value: strings.TrimPrefix(t.Value, "_:"),
		}
	default:
		jt := jsonResultTerm{
			Type:  "literal",
			Value: t.Value,
		}
		if t.Language != "" {
			jt.XMLLang = t.Language
		} else if t.Datatype != "" && t.Datatype != sparql.XSDStringIRI {
			jt.Datatype = t.Datatype
		}
		return jt
	}
}

const sparqlXMLNS = "http://www.w3.org/2005/sparql-results#"

type xmlVariable struct {
	Name string `xml:"name,attr"`
}

type xmlHead struct {
	Variables []xmlVariable `xml:"variable,omitempty"`
}

type xmlLiteral struct {
	Datatype string `xml:"datatype,attr,omitempty"`
	Lang     string `xml:"xml:lang,attr,omitempty"`
	Value    string `xml:",chardata"`
}

type xmlBinding struct {
	Name    string      `xml:"name,attr"`
	URI     *string     `xml:"uri,omitempty"`
	BNode   *string     `xml:"bnode,omitempty"`
	Literal *xmlLiteral `xml:"literal,omitempty"`
}

type xmlResultRow struct {
	Bindings []xmlBinding `xml:"binding"`
}

type xmlResults struct {
	Results []xmlResultRow `xml:"result"`
}

type xmlSPARQLDoc struct {
	XMLName xml.Name    `xml:"sparql"`
	XMLNS   string      `xml:"xmlns,attr"`
	Head    xmlHead     `xml:"head"`
	Results *xmlResults `xml:"results,omitempty"`
	Boolean *bool       `xml:"boolean,omitempty"`
}

func writeSPARQLXML(w io.Writer, res *sparql.Result) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	doc := xmlSPARQLDoc{
		XMLNS: sparqlXMLNS,
	}
	if res.Form == sparql.QueryAsk {
		b := res.AskResult
		doc.Boolean = &b
	} else {
		doc.Head.Variables = make([]xmlVariable, 0, len(res.Variables))
		for _, v := range res.Variables {
			doc.Head.Variables = append(doc.Head.Variables, xmlVariable{Name: v})
		}
		rows := make([]xmlResultRow, 0, len(res.Solutions))
		for _, sol := range res.Solutions {
			bindings := make([]xmlBinding, 0, len(sol))
			for _, v := range res.Variables {
				term, ok := sol[v]
				if !ok {
					continue
				}
				bindings = append(bindings, toXMLBinding(v, term))
			}
			rows = append(rows, xmlResultRow{Bindings: bindings})
		}
		doc.Results = &xmlResults{Results: rows}
	}
	enc := xml.NewEncoder(w)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func toXMLBinding(name string, t sparql.Term) xmlBinding {
	switch t.Kind {
	case sparql.TermIRI:
		val := t.Value
		return xmlBinding{Name: name, URI: &val}
	case sparql.TermBlankNode:
		val := strings.TrimPrefix(t.Value, "_:")
		return xmlBinding{Name: name, BNode: &val}
	default:
		lit := &xmlLiteral{Value: t.Value}
		if t.Language != "" {
			lit.Lang = t.Language
		} else if t.Datatype != "" && t.Datatype != sparql.XSDStringIRI {
			lit.Datatype = t.Datatype
		}
		return xmlBinding{Name: name, Literal: lit}
	}
}

func writeSPARQLCSV(w io.Writer, res *sparql.Result) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(res.Variables); err != nil {
		return err
	}
	row := make([]string, len(res.Variables))
	for _, sol := range res.Solutions {
		for i, v := range res.Variables {
			if term, ok := sol[v]; ok {
				row[i] = term.Value
			} else {
				row[i] = ""
			}
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeSPARQLTSV(w io.Writer, res *sparql.Result) error {
	var b strings.Builder
	for i, v := range res.Variables {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteByte('?')
		b.WriteString(v)
	}
	b.WriteByte('\n')
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}

	for _, sol := range res.Solutions {
		b.Reset()
		for i, v := range res.Variables {
			if i > 0 {
				b.WriteByte('\t')
			}
			if term, ok := sol[v]; ok {
				b.WriteString(formatTSVTerm(term))
			}
		}
		b.WriteByte('\n')
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	return nil
}

func formatTSVTerm(t sparql.Term) string {
	switch t.Kind {
	case sparql.TermIRI:
		return "<" + t.Value + ">"
	case sparql.TermBlankNode:
		return t.Value
	default:
		escaped := escapeTSVLiteral(t.Value)
		if t.Language != "" {
			return `"` + escaped + `"@` + t.Language
		}
		if t.Datatype != "" && t.Datatype != sparql.XSDStringIRI {
			return `"` + escaped + `"^^<` + t.Datatype + `>`
		}
		return `"` + escaped + `"`
	}
}

func escapeTSVLiteral(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
