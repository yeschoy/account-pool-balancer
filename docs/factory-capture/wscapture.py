import json, os, time
OUT=os.environ.get("CAP_OUT","/tmp/factory-re/capture.ndjson")
_fh=None
def log(rec):
    global _fh
    if _fh is None: _fh=open(OUT,"a",encoding="utf-8",buffering=1)
    _fh.write(json.dumps(rec,ensure_ascii=False,default=lambda o: o.decode('utf-8','backslashreplace') if isinstance(o,bytes) else repr(o))+"\n")
class WS:
    def websocket_start(self, flow):
        log({"kind":"ws_start","url":flow.request.pretty_url,
             "req_headers":[[k,v] for k,v in flow.request.headers.items(multi=True)]})
    def websocket_message(self, flow):
        try:
            m=flow.websocket.messages[-1]
            b=m.content
            if isinstance(b,bytes): b=b.decode("utf-8","backslashreplace")
            log({"kind":"ws_frame","url":flow.request.pretty_url,"from_client":bool(m.from_client),"len":len(b),"data":b})
        except Exception as e: log({"kind":"ws_error","error":repr(e)})
addons=[WS()]
