import sys, json, time, urllib.request
def ask(q, history=None):
    t0 = time.time()
    req = urllib.request.Request("http://127.0.0.1:8470/api/ask", data=json.dumps({"question": q, "history": history or []}).encode(), headers={"Content-Type": "application/json"})
    ans = ""
    print("=== Q:", q)
    with urllib.request.urlopen(req, timeout=300) as r:
        for line in r:
            e = json.loads(line)
            if e["type"] == "status": print("  [status]", e["text"][:180])
            elif e["type"] == "sources": print("  [sources]", "; ".join("%d %s (%s)" % (s["n"], s["title"], s["book"]) for s in e["sources"]))
            elif e["type"] == "token": ans += e["text"]
            elif e["type"] == "error": print("  [ERROR]", e["text"])
    print(ans)
    print("  (%.0fs)\n" % (time.time() - t0))
    return ans
qs = sys.argv[1:]
hist = []
for q in qs:
    a = ask(q, hist)
    hist += [{"role": "user", "content": q}, {"role": "assistant", "content": a}]
