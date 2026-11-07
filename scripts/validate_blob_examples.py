"""Offline documentation gate. Run through just bb just validate-blob-fixtures."""
import base64
import copy
import datetime as dt
import hashlib
import json
import pathlib
import re
import subprocess

from jsonschema import Draft202012Validator, ValidationError

root = pathlib.Path(__file__).resolve().parent.parent
protocol = root / "docs/protocol"
schema = json.loads((protocol / "envelope.schema.json").read_text())
attachment = Draft202012Validator({"$ref": "#/$defs/attachment", "$defs": schema["$defs"]})


def require(condition, message):
    if not condition:
        raise ValueError(message)


def instant(value):
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def no_capability(value):
    if isinstance(value, dict):
        require(not ({"url", "upload", "complete_url", "blob_url", "token"} & value.keys()),
                "capability in persistent reference or error")
        for child in value.values():
            no_capability(child)
    elif isinstance(value, list):
        for child in value:
            no_capability(child)


def main():
    data = json.loads((protocol / "examples/11-r2-transfers.json").read_text())
    sha = data["request"]["sha256"]
    require(sha == hashlib.sha256(b"x").hexdigest(), "synthetic file hash")
    require(data["checksum_base64"] == base64.b64encode(bytes.fromhex(sha)).decode(), "checksum encoding")
    require(set(data["request"]) == {"size", "sha256", "media_type"}, "upload request fields")
    require(data["request"]["size"] == 1, "one-byte example")
    fields = {"blob_id", "sha256", "sha256_verified", "integrity", "size", "retention_class", "expires_at"}
    for name in ("finite", "keep"):
        case = data[name]
        grant, complete = case["grant"], case["complete"]
        require(set(grant) == fields | {"upload", "complete_url"}, "exact grant fields")
        require(set(complete) == fields, "exact completion fields")
        require(grant["sha256"] == complete["sha256"] == sha, "hash binding")
        require(grant["size"] == data["request"]["size"], "grant size binding")
        require(grant["complete_url"] == "/v1/blobs/uploads/" + grant["blob_id"] + "/complete", "completion path")
        require(set(grant["upload"]) == {"method", "url", "headers", "expires_at"}, "exact PUT capability fields")
        require(grant["upload"]["url"] == "https://r2.example.invalid/REDACTED-PUT", "inert PUT placeholder")
        for field in fields - {"sha256_verified", "integrity"}:
            require(grant[field] == complete[field], "completion changed recorded " + field)
        require(grant["sha256_verified"] is False and complete["sha256_verified"] is True,
                "verification only after HEAD completion")
        for ref in (grant, complete):
            require(ref["integrity"] == {"algorithm": "sha256", "value": sha,
                                         "verified": ref["sha256_verified"]}, "integrity binding")
        issued = instant(case["issued_at"])
        require(instant(grant["upload"]["expires_at"]) - issued == dt.timedelta(seconds=300), "PUT TTL")
        require(grant["upload"]["method"] == "PUT", "PUT method")
        headers = {k.lower(): v for k, v in grant["upload"]["headers"].items()}
        require(headers == {"content-length": "1", "content-type": "application/octet-stream",
                           "content-disposition": "attachment", "cache-control": "private, no-store",
                           "if-none-match": "*", "x-amz-checksum-sha256": data["checksum_base64"],
                           "x-amz-meta-vecnet-checksum-profile": "sha256",
                           "x-amz-meta-vecnet-checksum": data["checksum_base64"]}, "signed PUT headers")
        for index, download in enumerate(case["downloads"]):
            require(set(download) == fields | {"url", "method", "media_type", "object_expires_at"}, "exact GET fields")
            require(download["url"] == "https://r2.example.invalid/REDACTED-GET", "inert GET placeholder")
            require(download["size"] == complete["size"] and download["sha256"] == sha and
                    download["integrity"] == complete["integrity"] and download["sha256_verified"] is True,
                    "GET verified reference")
            minted = instant(case["get_at"][index])
            deadline = instant(download["expires_at"])
            require(download["method"] == "GET" and download["blob_id"] == grant["blob_id"], "GET scope")
            require(download["object_expires_at"] == grant["expires_at"], "immutable object deadline")
            require(download["retention_class"] == grant["retention_class"], "immutable class")
            ttl = 600 if grant["expires_at"] is None else min(600, int((instant(grant["expires_at"]) - minted).total_seconds()))
            require(deadline - minted == dt.timedelta(seconds=ttl), "GET floor cap")
        require(instant(case["get_at"][1]) > instant(case["downloads"][0]["expires_at"]), "re-mint after URL expiry")
        require(case["surfaces"] == ["MCP", "HTTP", "CLI"], "repeatable get surfaces")
        require(case["preference_after_grant"] != grant["retention_class"], "preference edit scenario")
        ref = case["attachment"]
        attachment.validate(ref)
        no_capability(ref)
        no_capability(case["state"])
        require(case["state"] == {"state": "available", **{k: complete[k] for k in
                ("retention_class", "expires_at", "sha256_verified", "integrity")}}, "state projection")
        require(all(ref[k] == complete[k] for k in fields), "reference matches completion")
        bad = copy.deepcopy(ref)
        bad.pop("blob_id")
        require(not attachment.is_valid(bad), "schema must require blob_id with R2 fields")
        bad = copy.deepcopy(ref)
        bad["expires_at"] = "2026-11-08T00:00:00.000Z" if name == "keep" else None
        require(not attachment.is_valid(bad), "schema must distinguish keep null from finite deadline")
        require((grant["expires_at"] is None) == (name == "keep"), "explicit keep null")
        if name == "finite":
            require(grant["retention_class"] == "1d" and instant(grant["expires_at"]) - issued == dt.timedelta(days=1), "24 h default")
        else:
            require(grant["retention_class"] == "keep", "keep class")
    no_capability(data["message"])
    Draft202012Validator({"$ref": "#/$defs/message", "$defs": schema["$defs"]}).validate(data["message"])
    near = data["near_deadline"]
    require(instant(near["download"]["expires_at"]) == instant(near["get_at"]).replace(microsecond=0) +
            dt.timedelta(seconds=int((instant(near["download"]["object_expires_at"]) - instant(near["get_at"])).total_seconds())),
            "near-deadline floor rounding")
    require(data["subsecond_deadline"]["status"] == 410 and data["subsecond_deadline"]["code"] == "blob_expired",
            "subsecond lifetime refuses URL")
    require(data["message"]["attachments"] == [data["finite"]["attachment"]], "reference-only message")
    errors = json.loads((protocol / "examples/12-r2-errors.json").read_text())
    expected = {"blob_expired": 410, "blob_purged": 410, "blob_url_rate_limited": 429,
                "blob_storage_quota_exceeded": 429, "prefs_confirmation_required": 409}
    require({e["body"]["error"]["code"]: e["status"] for e in errors} == expected, "error coverage")
    for e in errors:
        no_capability(e["body"])
        if e["status"] == 429:
            retry = e["body"]["error"]["details"]["retry_after_seconds"]
            require(type(retry) is int and retry > 0 and e["headers"]["Retry-After"] == str(retry), "retry detail/header")
    # This scan intentionally reports paths only, never the rejected values.
    for path in (protocol / "examples").rglob("*.json"):
        raw = path.read_text()
        require(not re.search(r"(?i)X-Amz-(?:Signature|Credential)=|AWS4-HMAC-SHA256|-----BEGIN .*PRIVATE KEY|(?:AKIA|ASIA)[A-Z0-9]{16}", raw),
                "presign or secret in " + str(path.relative_to(root)))
        require(not re.search(r'(?i)"(?:secret_access_key|access_key_id|authorization|password)"\s*:', raw),
                "credential field in " + str(path.relative_to(root)))
    sentence = "URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired."
    for name in ("docs/PROTOCOL.md", "docs/DEPLOY.md", "README.md"):
        require(sentence in (root / name).read_text(), "missing repeatable-get guidance in " + name)
    deploy = (root / "docs/DEPLOY.md").read_text()
    blocks = [json.loads(block) for block in re.findall(r"```json\n(.*?)\n\s*```", deploy, re.S)]
    rules = next(block["Rules"] for block in blocks if isinstance(block, dict) and "Rules" in block)
    require({r["Filter"]["Prefix"]: r["Expiration"]["Days"] for r in rules} == {
        "attachments/v1/exp/" + cls + "/": days for cls, days in
        (("1h", 1), ("6h", 1), ("12h", 1), ("1d", 1), ("7d", 7), ("30d", 30))}, "six class-first lifecycle rules")
    cors = next(block[0] for block in blocks if isinstance(block, list) and block and "AllowedOrigins" in block[0])
    require(cors["AllowedOrigins"] == ["https://vecnet.xk.io"] and set(cors["AllowedMethods"]) == {"PUT", "GET", "HEAD"}, "exact CORS origin/methods")
    require({"if-none-match", "x-amz-checksum-sha256", "range"} <= set(cors["AllowedHeaders"]), "CORS signed headers and Range")
    require({"Content-Range", "ETag", "x-amz-checksum-sha256"} <= set(cors["ExposeHeaders"]), "CORS response exposure")
    require("multipart-abort-only rule (7 days)" in deploy, "preserve multipart abort")
    for name in (".dockerignore", ".gitignore"):
        require({"**/r2-test.env", "**/r2-spike*.env", "**/r2-credentials*"} <=
                set((root / name).read_text().splitlines()), "operator credential exclusions in " + name)
    deps = subprocess.run(["go", "list", "-deps", "./cmd/vecnet"], cwd=root, check=True, capture_output=True, text=True).stdout.splitlines()
    require(not any(p.startswith("modernc.org/sqlite") for p in deps), "CLI imports SQLite")
    print("OK R2 documentation fixtures, reference schema, TTLs, retry errors, fixture hygiene and CLI dependency gate")


if __name__ == "__main__":
    try:
        main()
    except ValidationError:
        raise SystemExit("FAIL R2 documentation fixtures: envelope schema mismatch") from None
    except (ValueError, KeyError, FileNotFoundError) as error:
        raise SystemExit("FAIL R2 documentation fixtures: " + str(error)) from None
