#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Build the specification cases of testdata/transcripts/spec from the
OpenAPI documents committed there.

The OpenAPI documents of OpenADR 3 carry examples on properties, not whole
objects. A case is the instance of one component schema made, recursively,
of those examples, the way OpenAPI tools render a sample: for every
property the first of `example`, `default` (null included), the first
`enum` value; objects and arrays from their parts; `allOf` merged, `oneOf`
and `anyOf` by their first branch. A property for which none of these
exists is left out, and listed in the case as `missing`, so that what the
specification does not say is not made up.

Input is the JSON form of a YAML document (yq -o=json); the script reads no
YAML itself so that it needs nothing beyond the Python standard library.

    yq -o=json spec/3.1.0/openadr3.yaml | python3 -I scripts/transcripts-spec-cases.py 3.1.0 > spec/3.1.0/cases.json
"""

import json
import sys

# The objectType of the VTN's representation of each object. The documents
# compose an object of objectMetadata and a request schema with allOf, and
# both name an objectType: the object's own is the one a VTN sends.
OBJECT_TYPES = {
    "program": "PROGRAM",
    "event": "EVENT",
    "report": "REPORT",
    "subscription": "SUBSCRIPTION",
    "ven": "VEN",
    "resource": "RESOURCE",
}


def resolve(doc, ref):
    node = doc
    for part in ref.lstrip("#/").split("/"):
        node = node[part]
    return node


class Builder:
    def __init__(self, doc, populated):
        self.doc = doc
        self.populated = populated
        self.missing = []

    def build(self, schema, path, depth=0):
        """Return (present, value)."""
        if depth > 12:
            self.missing.append(path + " (recursion)")
            return False, None
        if "$ref" in schema:
            return self.build(resolve(self.doc, schema["$ref"]), path, depth + 1)
        if "example" in schema:
            return True, schema["example"]
        if "allOf" in schema:
            merged = {}
            present = False
            for part in schema["allOf"]:
                ok, v = self.build(part, path, depth + 1)
                if ok and isinstance(v, dict):
                    merged.update(v)
                    present = True
            if "properties" in schema:
                ok, v = self.object(schema, path, depth)
                if ok:
                    merged.update(v)
                    present = True
            return present, merged
        for key in ("oneOf", "anyOf"):
            if key in schema:
                return self.build(schema[key][0], path, depth + 1)
        structured = schema.get("type") in ("array", "object") or "properties" in schema or "items" in schema
        if "default" in schema and not (self.populated and schema["default"] is None and structured):
            return True, schema["default"]
        if "enum" in schema:
            return True, schema["enum"][0]
        kind = schema.get("type")
        if kind == "object" or "properties" in schema:
            return self.object(schema, path, depth)
        if kind == "array":
            ok, v = self.build(schema.get("items", {}), path + "/0", depth + 1)
            return True, [v] if ok else []
        self.missing.append(path or "/")
        return False, None

    def object(self, schema, path, depth):
        out = {}
        for name, prop in schema.get("properties", {}).items():
            ok, v = self.build(prop, path + "/" + name, depth + 1)
            if ok:
                out[name] = v
        return True, out


def main():
    version = sys.argv[1]
    doc = json.load(sys.stdin)
    cases = []
    # Two variants of each schema: "defaults" takes a default where there is
    # one (null for most optional arrays), "populated" builds an optional
    # array or object whose default is null from its items instead.
    for name in sorted(doc["components"]["schemas"]):
        seen = []
        for variant in ("defaults", "populated"):
            b = Builder(doc, variant == "populated")
            ok, value = b.build(doc["components"]["schemas"][name], "")
            if not ok:
                continue
            if name in OBJECT_TYPES and isinstance(value, dict):
                value["objectType"] = OBJECT_TYPES[name]
            if value in seen:
                continue
            seen.append(value)
            cases.append({"schema": name, "variant": variant, "instance": value, "missing": sorted(set(b.missing))})
    json.dump({"version": version, "cases": cases}, sys.stdout, indent=2, sort_keys=False)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
