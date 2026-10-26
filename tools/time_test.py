import sys, json, time, urllib.request
q = sys.argv[1]
t0 = time.time(); last = t0; first_tok = None
req = urllib.request.Request("http://127.0.0.1:8470/api/ask", data=json.dumps({"question": q, "history": []}).encode(), headers={"Content-Type": "application/json"})
print("Q:", q)
with urllib.request.urlopen(req, timeout=600) as r:
    for line in r:
        e = json.loads(line); now = time.time()
        if e["type"] == "status":
            print("  %5.1fs (+%4.1f)  %s" % (now - t0, now - last, e["text"][:70])); last = now
        elif e["type"] == "token" and first_tok is None:
            first_tok = now; print("  %5.1fs (+%4.1f)  first words of answer" % (now - t0, now - last)); last = now
print("  %5.1fs (+%4.1f)  done" % (time.time() - t0, time.time() - last))
