package searchgateway

import (
 "bytes"
 "encoding/json"
 "fmt"
 "io"
)

type object map[string]json.RawMessage
func marshal(v any) json.RawMessage {b,_:=json.Marshal(v);return b}
func str(b json.RawMessage)string {var s string;_ = json.Unmarshal(b,&s);return s}
func array(b json.RawMessage)([]json.RawMessage,error){var a []json.RawMessage;if json.Unmarshal(b,&a)!=nil || a==nil{return nil,fmt.Errorf("expected array")};return a,nil}
func decodeObject(b []byte)(object,error) {
 if err:=validateJSON(b);err!=nil{return nil,err}
 var o object;if json.Unmarshal(b,&o)!=nil || o==nil{return nil,fmt.Errorf("expected JSON object")};return o,nil
}
// Reject duplicate keys, including escaped spellings, before consuming fields.
// RawMessage keeps tool arguments and unrelated numeric fields lossless.
func validateJSON(b []byte)error {
 d:=json.NewDecoder(bytes.NewReader(b));d.UseNumber()
 var walk func(int)error
 walk=func(depth int)error {
  if depth>64{return fmt.Errorf("JSON nesting limit")}
  t,e:=d.Token();if e!=nil{return fmt.Errorf("invalid JSON")}
  delim,ok:=t.(json.Delim);if !ok{return nil}
  switch delim {
  case '{':seen:=map[string]bool{};for d.More(){k,e:=d.Token();if e!=nil{return fmt.Errorf("invalid JSON")};key,ok:=k.(string);if !ok || seen[key]{return fmt.Errorf("ambiguous JSON field")};seen[key]=true;if e=walk(depth+1);e!=nil{return e}}
  case '[':for d.More(){if e:=walk(depth+1);e!=nil{return e}}
  default:return fmt.Errorf("invalid JSON")
  }
  _,e=d.Token();if e!=nil{return fmt.Errorf("invalid JSON")};return nil
 }
 if e:=walk(0);e!=nil{return e};if _,e:=d.Token();e!=io.EOF{return fmt.Errorf("trailing JSON")};return nil
}
func clone(o object)object {n:=make(object,len(o));for k,v:=range o {n[k]=v};return n}

type gatewayError struct {Status int;Code,Message string}
func(e *gatewayError)Error()string{return e.Code}
func invalid(code,msg string)error{return &gatewayError{400,code,msg}}
func upstream(code string)error{return &gatewayError{502,code,"Upstream response could not be completed; the request was not replayed"}}
