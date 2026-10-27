import re

files = [
    r'D:/Projects/Nine Router Go Fork/README.md',
    r'D:/Projects/Nine Router Go Fork/docs/ARCHITECTURE.md',
    r'D:/Projects/Nine Router Go Fork/start.cmd',
    r'D:/Projects/Nine Router Go Fork/docs/OPERATIONS.md',
]
repl = [
    ('<your-router-key>', '<your-router-key>'),
    ('C:\\\\Users\\\\Unkno\\\\Music\\\\api\\\\rotation', '<9router-install>\\\\rotation'),
    ('C:\\Users\\Unkno\\Music\\api\\rotation', '<9router-install>\\rotation'),
    ('C:\\\\Users\\\\Unkno\\\\AppData\\\\Roaming\\\\npm\\\\node_modules\\\\opencode-ai', '<npm-global>\\opencode-ai'),
    ('C:\\Users\\Unkno\\AppData\\Roaming\\npm\\node_modules\\opencode-ai', '<npm-global>\\opencode-ai'),
    ('C:\\\\Users\\\\Unkno', '<user-profile>'),
    ('C:\\Users\\Unkno', '<user-profile>'),
]
for f in files:
    try:
        t = open(f, encoding='utf-8').read()
    except FileNotFoundError:
        print('skip (missing):', f)
        continue
    n = 0
    for pat, to in repl:
        t2 = t.replace(pat, to)  # literal replace — no regex escaping issues
        if t2 != t:
            n += 1
        t = t2
    open(f, 'w', encoding='utf-8', newline='').write(t)
    print(f.split('/')[-1], '- scrubbed', n, 'patterns')
