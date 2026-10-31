import json, sys, pathlib
from jsonschema import Draft202012Validator

root = pathlib.Path(sys.argv[1])
schema = json.loads((root / "envelope.schema.json").read_text())
Draft202012Validator.check_schema(schema)

def v(defname, obj, label):
    sub = {"$schema": schema["$schema"], "$ref": f"#/$defs/{defname}", "$defs": schema["$defs"]}
    errs = sorted(Draft202012Validator(sub).iter_errors(obj), key=lambda e: list(e.path))
    status = "OK " if not errs else "ERR"
    print(f"{status} {label} as {defname}")
    for e in errs[:8]:
        print("     ", "/".join(map(str, e.path)) or "<root>", "->", e.message[:160])
    return not errs

ex = root / "examples"
def load(name):
    d = json.loads((ex / name).read_text()); d.pop("$comment", None); return d

ok = True
ok &= v("sendRequest", load("01-send-request.json"), "01")
ok &= v("delivery", load("02-delivery-direct.json"), "02")
ok &= v("delivery", load("03-delivery-group-hold.json"), "03")
ok &= v("sendRequest", load("04-approval-request.json"), "04")
ok &= v("sendRequest", load("05-job-status.json"), "05")
ok &= v("delivery", load("08-github-ingress-delivery.json"), "08")
for i, e in enumerate(load("06-events-stream.json")["events"]):
    ok &= v("event", e, f"06[{i}]")
# negative checks
bad = load("02-delivery-direct.json"); bad["message"]["kind"] = "job_status"
ok &= (not v("delivery", bad, "NEG kind=job_status without job_status (expect ERR)"))
bad2 = load("01-send-request.json"); bad2["recipients"] = ["Mira"]
ok &= (not v("sendRequest", bad2, "NEG name-addressed recipient (expect ERR)"))
print("ALL GOOD" if ok else "FAILURES")
sys.exit(0 if ok else 1)
