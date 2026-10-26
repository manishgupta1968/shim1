#!/usr/bin/env python3
"""Convert plain-data TypeScript schema modules (ONDC log-validation-utility) to JSON Schema files.

This is a *parser*, not an evaluator: it never executes the TypeScript. It understands exactly the
constructs those files use: `export const X = <literal>`, objects/arrays/strings/numbers/booleans/null,
trailing commas, comments, object shorthand `{ name }`, spreads `...name`, `...new Set([...])`,
identifier references to other top-level consts (same file or imported from a sibling module).
Anything else raises an error.
"""
import json
import os
import re
import sys

TOKEN = re.compile(
    r"""
    (?P<ws>\s+)
  | (?P<lc>//[^\n]*)
  | (?P<bc>/\*.*?\*/)
  | (?P<str>'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"|`(?:\\.|[^`\\])*`)
  | (?P<num>-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)
  | (?P<ident>[A-Za-z_$][\w$]*)
  | (?P<spread>\.\.\.)
  | (?P<punct>[{}\[\](),:;=<>])
    """,
    re.X | re.S,
)


def tokenize(src):
    pos, out = 0, []
    while pos < len(src):
        m = TOKEN.match(src, pos)
        if not m:
            raise SyntaxError("cannot tokenize at %r" % src[pos:pos + 40])
        pos = m.end()
        kind = m.lastgroup
        if kind in ("ws", "lc", "bc"):
            continue
        out.append((kind, m.group(kind)))
    return out


_SIMPLE = {"n": "\n", "t": "\t", "r": "\r", "b": "\b", "f": "\f", "v": "\v", "0": "\0"}


def unquote(s):
    """Decode a JS string literal body with JS escape semantics (unknown escapes drop the backslash)."""
    body = s[1:-1]
    out, i = [], 0
    while i < len(body):
        c = body[i]
        if c != "\\":
            out.append(c)
            i += 1
            continue
        i += 1
        e = body[i]
        if e in _SIMPLE:
            out.append(_SIMPLE[e])
            i += 1
        elif e == "x":
            out.append(chr(int(body[i + 1:i + 3], 16)))
            i += 3
        elif e == "u":
            if body[i + 1] == "{":
                j = body.index("}", i)
                out.append(chr(int(body[i + 2:j], 16)))
                i = j + 1
            else:
                out.append(chr(int(body[i + 1:i + 5], 16)))
                i += 5
        elif e == "\n":
            i += 1
        else:
            out.append(e)
            i += 1
    return "".join(out)


class Parser:
    def __init__(self, toks, env):
        self.t = toks
        self.i = 0
        self.env = env

    def peek(self, k=0):
        return self.t[self.i + k] if self.i + k < len(self.t) else (None, None)

    def eat(self, val=None):
        kind, v = self.t[self.i]
        if val is not None and v != val:
            raise SyntaxError("expected %r got %r at token %d" % (val, v, self.i))
        self.i += 1
        return kind, v

    def value(self):
        kind, v = self.peek()
        if v == "{":
            return self.obj()
        if v == "[":
            return self.arr()
        if kind == "str":
            self.eat()
            return unquote(v)
        if kind == "num":
            self.eat()
            return float(v) if "." in v or "e" in v.lower() else int(v)
        if kind == "ident":
            self.eat()
            if v == "true":
                return True
            if v == "false":
                return False
            if v == "null":
                return None
            if v in self.env:
                return self.env[v]
            raise NameError("unknown identifier %s" % v)
        raise SyntaxError("unexpected token %r" % (v,))

    def obj(self):
        self.eat("{")
        o = {}
        while self.peek()[1] != "}":
            kind, v = self.peek()
            if kind == "spread":
                self.eat()
                o.update(self.value())
            else:
                self.eat()
                key = unquote(v) if kind == "str" else v
                if self.peek()[1] == ":":
                    self.eat(":")
                    o[key] = self.value()
                else:  # shorthand
                    o[key] = self.env[key]
            if self.peek()[1] == ",":
                self.eat(",")
        self.eat("}")
        return o

    def arr(self):
        self.eat("[")
        a = []
        while self.peek()[1] != "]":
            kind, v = self.peek()
            if kind == "spread":
                self.eat()
                if self.peek()[1] == "new":
                    self.eat()  # new
                    self.eat("Set")
                    self.eat("(")
                    inner = self.value()
                    self.eat(")")
                    seen = []
                    for x in inner:
                        if x not in seen:
                            seen.append(x)
                    a.extend(seen)
                else:
                    a.extend(self.value())
            else:
                a.append(self.value())
            if self.peek()[1] == ",":
                self.eat(",")
        self.eat("]")
        return a


def load_consts(path, loaded=None):
    """Parse every top-level `[export] const NAME = <literal>` in a TS file into a dict."""
    loaded = loaded if loaded is not None else {}
    src = open(path, encoding="utf8").read()
    env = {}
    base = os.path.dirname(path)
    for m in re.finditer(r"^import\s*\{([^}]*)\}\s*from\s*'([^']+)'", src, re.M):
        names = [n.strip() for n in m.group(1).split(",") if n.strip()]
        dep = os.path.normpath(os.path.join(base, m.group(2)))
        if not dep.endswith(".ts"):
            dep += ".ts"
        if dep not in loaded:
            loaded[dep] = load_consts(dep, loaded)
        for n in names:
            env[n] = loaded[dep][n]
    body = re.sub(r"^import .*$", "", src, flags=re.M)
    toks = tokenize(body)
    i = 0
    while i < len(toks):
        if toks[i][1] in ("export", "const") :
            j = i
            if toks[j][1] == "export":
                j += 1
            if toks[j][1] == "const" and toks[j + 1][0] == "ident" and toks[j + 2][1] in ("=", ":"):
                name = toks[j + 1][1]
                k = j + 2
                if toks[k][1] == ":":  # skip a type annotation up to '='
                    while toks[k][1] != "=":
                        k += 1
                p = Parser(toks, env)
                p.i = k + 1
                try:
                    env[name] = p.value()
                except (NameError, SyntaxError) as e:
                    # not a literal (e.g. helper constants we do not need): skip
                    print("skip const %s in %s: %s" % (name, os.path.basename(path), e), file=sys.stderr)
                i = p.i
                continue
        i += 1
    return env


def fix_oneof_ifthen(node):
    """The ONDC validator writes `oneOf: [{if: A, then: B}, ...]` meaning "if A then B" per branch.
    A branch whose `if` is false is vacuously valid, so under strict `oneOf` (exactly one branch) real
    payloads fail. When every branch is a pure if/then the intent is one implication per branch, so we
    use `allOf`; when only some branches are if/then we relax `oneOf` to `anyOf`."""
    if isinstance(node, dict):
        for k in list(node):
            fix_oneof_ifthen(node[k])
        v = node.get("oneOf")
        if isinstance(v, list) and v and any(isinstance(x, dict) and "if" in x for x in v):
            pure = all(isinstance(x, dict) and "if" in x and set(x) <= {"if", "then", "else", "type"} for x in v)
            key = "allOf" if pure else "anyOf"
            node[key] = node.get(key, []) + node.pop("oneOf")
    elif isinstance(node, list):
        for x in node:
            fix_oneof_ifthen(x)


# JavaScript (Ajv) accepts regex features and spellings that Go's RE2 engine (used by ONIX) does not.
# Only two such patterns occur in the official retail schemas, so they are rewritten explicitly.
_LOOKAHEAD_NON_BLANK = "^(?!s*$).+"  # intended as "not blank" (a JS string drops the backslash of \\s)


def fix_patterns(node):
    """Rewrite the JS-only regexes in `pattern` keywords so they compile under RE2 with the same intent."""
    if isinstance(node, dict):
        for k, v in list(node.items()):
            if k == "pattern" and isinstance(v, str):
                if v == _LOOKAHEAD_NON_BLANK:
                    node[k] = r"^\s*\S"
                else:
                    m = re.match(r"^/(.*)/([a-z]*)$", v, re.S)
                    if m:  # a regex literal written inside a string, e.g. '/^ABW|AFG$/ix'
                        node[k] = ("(?i)" if "i" in m.group(2) else "") + m.group(1)
            else:
                fix_patterns(v)
    elif isinstance(node, list):
        for x in node:
            fix_patterns(x)


def drop_misplaced_required(node):
    """Some official schemas list `required: [...]` *inside* `properties`, where JSON Schema expects a
    sub-schema (often one nesting level away from the object it was meant for). Ajv ignores it, so the
    official validator never enforces it, while strict validators reject the whole schema. Drop it, which
    keeps the official validator's effective behaviour."""
    if isinstance(node, dict):
        props = node.get("properties")
        if isinstance(props, dict) and isinstance(props.get("required"), list):
            del props["required"]
        for v in node.values():
            drop_misplaced_required(v)
    elif isinstance(node, list):
        for x in node:
            drop_misplaced_required(x)


# Official validator source for each retail action: action -> "path under schema/Retail:ExportName".
RETAIL_SOURCES = {
    "search": "RET/search.ts:searchSchema",
    "on_search": "RET/on_search.ts:onSearchSchema",
    "select": "RET/select.ts:selectSchema",
    "on_select": "RET/on_select.ts:onSelectSchema",
    "init": "RET/init.ts:initSchema",
    "on_init": "RET/on_init.ts:onInitSchema",
    "confirm": "RET/confirm.ts:confirmSchema",
    "on_confirm": "RET/on_confirm.ts:onConfirmSchema",
    "status": "Status/status.ts:statusSchema",
    "on_status": "Status/on_status.ts:onStatusSchema",
    "track": "Track/track.ts:trackSchema",
    "on_track": "Track/on_track.ts:onTrackSchema",
    "update": "Update/update.ts:updateSchema",
    "on_update": "Update/on_update.ts:onUpdateSchema",
    "cancel": "Cancel/cancel.ts:cancelSchema",
    "on_cancel": "Cancel/onCancel.ts:onCancelSchema",
}


def convert_schema(src_dir, action, spec):
    """Parse one exported schema constant and return it as a draft-07 JSON Schema dict."""
    rel, name = spec.split(":")
    schema = dict(load_consts(os.path.join(src_dir, rel))[name])
    fix_oneof_ifthen(schema)
    fix_patterns(schema)
    drop_misplaced_required(schema)
    out = {"$schema": "http://json-schema.org/draft-07/schema#", "$id": action}
    out.update({k: v for k, v in schema.items() if k not in ("$schema", "$id")})
    return out


def main():
    """Usage: ts2json.py <log-validation-utility>/schema/Retail <out-dir>  (converts all RETAIL_SOURCES)."""
    src_dir, out_dir = sys.argv[1], sys.argv[2]
    os.makedirs(out_dir, exist_ok=True)
    for action, spec in RETAIL_SOURCES.items():
        with open(os.path.join(out_dir, action + ".json"), "w") as f:
            json.dump(convert_schema(src_dir, action, spec), f, indent=2, ensure_ascii=False)
            f.write("\n")
        print("ok", action)


if __name__ == "__main__":
    main()
