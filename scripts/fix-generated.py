#!/usr/bin/env python3
"""Post-generation fixes for oapi-codegen output.

Fixes known issues in the generated client code:
1. String literal assignments to *string pointer fields (discriminator defaults)
2. Duplicate type declarations (model vs response wrapper name collision)
"""

import re
import sys

FILE = sys.argv[1] if len(sys.argv) > 1 else "pkg/camunda/client.gen.go"

def main():
    with open(FILE, "r") as f:
        code = f.read()

    # 1. Add ptrStr helper if not already present
    if "func ptrStr(" not in code:
        code = code.replace(
            'const (\n\tBearerAuthScopes = "BearerAuth.Scopes"\n)',
            'const (\n\tBearerAuthScopes = "BearerAuth.Scopes"\n)\n\n// ptrStr returns a pointer to the given string value.\nfunc ptrStr(s string) *string { return &s }',
        )

    # 2. Fix string-to-*string discriminator assignments
    # Pattern: v.Type = "someValue" where Type is *string
    fixes = [
        ('v.Type = "userTask"', 'v.Type = ptrStr("userTask")'),
        ('v.Type = "adHocSubProcess"', 'v.Type = ptrStr("adHocSubProcess")'),
        ('v.Type = "TERMINATE_PROCESS_INSTANCE"', 'v.Type = ptrStr("TERMINATE_PROCESS_INSTANCE")'),
    ]
    for old, new in fixes:
        code = code.replace(old, new)

    # 3. Fix duplicate DeleteResourceResponse (response wrapper vs model collision)
    # Only apply this fix when there are TWO declarations of DeleteResourceResponse
    # (one as a model type, one as a response wrapper). If there's only one, no collision.
    decl_count = len(re.findall(r'^type DeleteResourceResponse struct \{', code, re.MULTILINE))
    if decl_count >= 2:
        # Find position of the second declaration (the response wrapper with Body/HTTPResponse).
        # Everything from that declaration onward uses the response wrapper name.
        matches = list(re.finditer(r'^type DeleteResourceResponse struct \{', code, re.MULTILINE))
        split_pos = matches[1].start()

        # Split code at the second declaration
        before = code[:split_pos]
        after = code[split_pos:]

        # In the "after" section, rename ALL occurrences of DeleteResourceResponse → DeleteResourceResp
        after = after.replace('DeleteResourceResponse', 'DeleteResourceResp')

        # In the "before" section, also rename response-wrapper references that appear
        # before the type declaration (e.g. ParseDeleteResourceResponse, WithResponse return types)
        before = re.sub(
            r'ParseDeleteResourceResponse\b',
            'ParseDeleteResourceResp',
            before,
        )
        # Also fix ClientWithResponsesInterface and similar forward references
        before = re.sub(
            r'\*DeleteResourceResponse\b(?![\s\S]*?struct)',
            '*DeleteResourceResp',
            before,
        )

        code = before + after

    with open(FILE, "w") as f:
        f.write(code)

    print(f"Post-generation fixes applied to {FILE}")

if __name__ == "__main__":
    main()
