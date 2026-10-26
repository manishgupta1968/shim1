#!/usr/bin/env python3
"""Rebuild beckn-onix/schemas.zip for the ONDC end-to-end tests.

What it changes (everything else is copied byte-for-byte, entry order and timestamps kept):

1. Fixes three defects in the shipped schemas:
   * core/v1.1.0/status.json and track.json declared `order_id` (and `ref_id`) as an Order *object*.
     Beckn core 1.1.0 defines them as strings, and ondc_trv10 status.json already requires a string,
     so /status could never validate and /track only validated with an unrealistic object.
   * ondc_trv10/v2.0.0/on_confirm.json carried a `$ref` to `confirm.json#/allOf/6` ("order must not
     have an id"), copied from confirm. on_confirm is where the BPP assigns order.id.
2. Adds `ondc_ret14/v1.2.5/<20 actions>.json` for ONDC Retail (core_version 1.2.5):
   * 16 are converted, without executing any code, from the schemas ONDC publishes in
     https://github.com/ONDC-Official/log-validation-utility (ISC licence, branch 1.2.5, schema/Retail).
   * rating, on_rating, support, on_support are not covered by that validator. They are written here
     from the Beckn core 1.1.0 Rating / XInput / Support definitions and the retail context rules.

Usage:
    rebuild_schemas_zip.py --validator <path to log-validation-utility checkout> \
        [--zip beckn-onix/schemas.zip] [--out beckn-onix/schemas.zip]
"""
import argparse
import copy
import io
import json
import os
import sys
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import ts2json  # noqa: E402

DRAFT7 = "http://json-schema.org/draft-07/schema#"
RETAIL_DIR = "schemas/ondc_ret14/v1.2.5/"
ORDER_REF = "./definitions.json#/$defs/Order"

MANUAL_ACTIONS = ("rating", "on_rating", "support", "on_support")


def dumps(obj):
    # The shipped schemas use 2-space indentation and no trailing newline.
    return json.dumps(obj, indent=2, ensure_ascii=False).encode("utf-8")


def patch_core_order_id(raw, props):
    """Replace the Order-object $ref on the given message properties with a plain string."""
    d = json.loads(raw)
    msg = d["properties"]["message"]["properties"]
    for p in props:
        assert msg[p] == {"$ref": ORDER_REF}, (p, msg[p])
        msg[p] = {"type": "string"}
    return dumps(d)


def patch_on_confirm(raw):
    d = json.loads(raw)
    bad = d["allOf"][8]
    assert bad == {"$ref": "./confirm.json#/allOf/6"}, bad
    del d["allOf"][8]
    return dumps(d)


def retail_context(action, with_bpp):
    """The retail context rules, taken from the official retail track schema."""
    ctx = {
        "type": "object",
        "properties": {
            "domain": {"type": "string", "minLength": 1},
            "action": {"type": "string", "const": action},
            "core_version": {"type": "string", "enum": ["1.2.0", "1.2.5"], "minLength": 1},
            "bap_id": {"type": "string", "minLength": 1},
            "bap_uri": {"type": "string", "minLength": 1, "format": "url"},
            "bpp_id": {"type": "string", "minLength": 1},
            "bpp_uri": {"type": "string", "minLength": 1, "format": "url"},
            "transaction_id": {"type": "string", "minLength": 1},
            "message_id": {"type": "string", "minLength": 1},
            "city": {"type": "string", "minLength": 1},
            "country": {"type": "string", "const": "IND"},
            "timestamp": {"type": "string", "minLength": 1, "format": "rfc3339-date-time"},
            "ttl": {"type": "string", "minLength": 1, "format": "duration"},
        },
        "required": ["domain", "action", "country", "city", "core_version", "bap_id", "bap_uri",
                     "transaction_id", "message_id", "timestamp", "ttl"],
    }
    if with_bpp:
        ctx["required"] += ["bpp_id", "bpp_uri"]
    return ctx


def manual_schema(action):
    """rating / on_rating / support / on_support, written from the Beckn core 1.1.0 definitions."""
    nonempty = {"type": "string", "minLength": 1}
    messages = {
        "rating": {
            "type": "object",
            "properties": {
                "ratings": {
                    "type": "array",
                    "minItems": 1,
                    "items": {
                        "type": "object",
                        "properties": {
                            "rating_category": {
                                "type": "string",
                                "enum": ["Item", "Order", "Fulfillment", "Provider", "Agent", "Support"],
                            },
                            "id": nonempty,
                            "value": {"type": "string", "pattern": "^[1-5]$"},
                        },
                        "required": ["rating_category", "id", "value"],
                    },
                }
            },
            "required": ["ratings"],
        },
        "on_rating": {
            "type": "object",
            "properties": {
                "feedback_form": {
                    "type": "object",
                    "properties": {
                        "form": {
                            "type": "object",
                            "properties": {
                                "url": {"type": "string", "minLength": 1, "format": "url"},
                                "mime_type": {
                                    "type": "string",
                                    "enum": ["text/html", "application/html", "application/xml"],
                                },
                            },
                            "required": ["url", "mime_type"],
                        },
                        "required": {"type": "boolean"},
                    },
                    "required": ["form"],
                }
            },
            "required": ["feedback_form"],
        },
        "support": {
            "type": "object",
            "properties": {
                "support": {
                    "type": "object",
                    "properties": {
                        "ref_id": nonempty,
                        "callback_phone": nonempty,
                        "phone": nonempty,
                        "email": nonempty,
                        "url": nonempty,
                    },
                    "required": ["ref_id"],
                }
            },
            "required": ["support"],
        },
        "on_support": {
            "type": "object",
            "properties": {
                "support": {
                    "type": "object",
                    "properties": {
                        "phone": nonempty,
                        "email": nonempty,
                        "url": nonempty,
                    },
                    "anyOf": [{"required": ["phone"]}, {"required": ["email"]}, {"required": ["url"]}],
                }
            },
            "required": ["support"],
        },
    }
    return {
        "$schema": DRAFT7,
        "$id": action,
        "type": "object",
        "properties": {"context": retail_context(action, with_bpp=True), "message": messages[action]},
        "required": ["context", "message"],
    }


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--validator", required=True, help="path to a log-validation-utility checkout (branch 1.2.5)")
    ap.add_argument("--zip", default="schemas.zip", help="input schemas.zip")
    ap.add_argument("--out", default=None, help="output zip (default: overwrite --zip)")
    args = ap.parse_args()
    out_path = args.out or args.zip

    retail_src = os.path.join(args.validator, "schema", "Retail")
    new_files = {}
    for action, spec in ts2json.RETAIL_SOURCES.items():
        new_files[RETAIL_DIR + action + ".json"] = dumps(ts2json.convert_schema(retail_src, action, spec))
    for action in MANUAL_ACTIONS:
        new_files[RETAIL_DIR + action + ".json"] = dumps(manual_schema(action))

    patches = {
        "schemas/core/v1.1.0/status.json": lambda b: patch_core_order_id(b, ["ref_id", "order_id"]),
        "schemas/core/v1.1.0/track.json": lambda b: patch_core_order_id(b, ["order_id"]),
        "schemas/ondc_trv10/v2.0.0/on_confirm.json": patch_on_confirm,
    }

    buf = io.BytesIO()
    with zipfile.ZipFile(args.zip) as zin, zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zout:
        seen = set()
        for info in zin.infolist():
            data = zin.read(info.filename)
            if info.filename in patches:
                data = patches[info.filename](data)
                seen.add(info.filename)
            if info.filename.startswith(RETAIL_DIR) and not info.is_dir():
                continue  # re-created below
            zi = copy.copy(info)
            zout.writestr(zi, data, compress_type=zipfile.ZIP_DEFLATED)
        missing = set(patches) - seen
        assert not missing, "patch targets not found: %s" % sorted(missing)
        existing = {i.filename for i in zin.infolist()}
        for d in ("schemas/ondc_ret14/", RETAIL_DIR):
            if d not in existing:
                zout.writestr(zipfile.ZipInfo(d, date_time=(2026, 1, 1, 0, 0, 0)), b"")
        for name in sorted(new_files):
            zout.writestr(zipfile.ZipInfo(name, date_time=(2026, 1, 1, 0, 0, 0)), new_files[name],
                          compress_type=zipfile.ZIP_DEFLATED)
    with open(out_path, "wb") as f:
        f.write(buf.getvalue())
    print("wrote %s (%d retail schemas, %d patched)" % (out_path, len(new_files), len(patches)))


if __name__ == "__main__":
    main()
