import os
import re
import glob

ROOT = r"C:\Users\zakyh\Documents\Big Project\kitodock"

# 1. Controllers bound in main.go
main = open(os.path.join(ROOT, "desktop", "main.go"), encoding="utf-8").read()
bind_block = main[main.index("Bind: []interface{}{"):]
bind_block = bind_block[:bind_block.index("},")]
bound = re.findall(r"app\.(\w+)", bind_block)
print("BOUND controllers:", bound)

# Map field name -> controller struct name via app.go
app = open(os.path.join(ROOT, "desktop", "app.go"), encoding="utf-8").read()
field_to_struct = dict(re.findall(r"(\w+)\s+\*controllers\.(\w+)", app))
print()

# 2. Exported methods per controller (desktop/controllers/*.go)
ctrl_dir = os.path.join(ROOT, "desktop", "controllers")
struct_methods = {}  # struct -> set(methods)
for f in glob.glob(os.path.join(ctrl_dir, "*.go")):
    if f.endswith("_test.go"):
        continue
    src = open(f, encoding="utf-8").read()
    for m in re.finditer(r"func \((?:\w+ )?\*?(\w+)\) (\w+)\(", src):
        struct_methods.setdefault(m.group(1), set()).add(m.group(2))

# 3. Binding stubs vs reality
stub_dir = os.path.join(ROOT, "desktop", "frontend", "wailsjs", "go", "controllers")
problems = 0
for stub in sorted(glob.glob(os.path.join(stub_dir, "*.js"))):
    name = os.path.basename(stub)[:-3]
    src = open(stub, encoding="utf-8").read()
    funcs = set(re.findall(r"export function (\w+)\(", src))
    # find the controller struct: field with this name
    struct = field_to_struct.get(name)
    if struct is None:
        print("!! NO STRUCT for binding file", name)
        problems += 1
        continue
    if name not in bound:
        print("!! CONTROLLER NOT BOUND in main.go:", name)
        problems += 1
    methods = struct_methods.get(struct, set())
    for fn in sorted(funcs):
        if fn not in methods:
            print("!! STUB WITHOUT GO METHOD: %s.%s" % (name, fn))
            problems += 1
    # reverse: Go methods missing from stubs (only matters if frontend imports them)
print()

# 4. Frontend imports vs stub files (catch missing stubs / typos)
fe_root = os.path.join(ROOT, "desktop", "frontend", "src")
import_re = re.compile(r'from "(\.\./)+wailsjs/go/controllers/(\w+)"')
used = {}
for f in glob.glob(os.path.join(fe_root, "**", "*.tsx"), recursive=True) + \
         glob.glob(os.path.join(fe_root, "**", "*.ts"), recursive=True):
    src = open(f, encoding="utf-8", errors="replace").read()
    for m in import_re.finditer(src):
        used.setdefault(m.group(2), set()).add(os.path.relpath(f, fe_root))
# named imports
named_re = re.compile(r"import \{([^}]+)\} from \"(?:\.\./)+wailsjs/go/controllers/(\w+)\"")
for f in glob.glob(os.path.join(fe_root, "**", "*.tsx"), recursive=True) + \
         glob.glob(os.path.join(fe_root, "**", "*.ts"), recursive=True):
    src = open(f, encoding="utf-8", errors="replace").read()
    for m in named_re.finditer(src):
        for one in m.group(1).split(","):
            one = one.strip().split(" as ")[0].strip()
            if not one:
                continue
            stub_file = os.path.join(stub_dir, m.group(2) + ".js")
            if not os.path.exists(stub_file):
                print("!! FRONTEND IMPORTS MISSING STUB FILE:", m.group(2), "in", os.path.relpath(f, fe_root))
                problems += 1
                continue
            stub_src = open(stub_file, encoding="utf-8").read()
            if ("export function %s(" % one) not in stub_src:
                print("!! FRONTEND IMPORTS MISSING STUB FUNC: %s.%s in %s" % (m.group(2), one, os.path.relpath(f, fe_root)))
                problems += 1

print("controllers used by frontend:", sorted(used.keys()))
print()
print("TOTAL PROBLEMS:", problems)
