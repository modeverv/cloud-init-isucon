import sys,collections
n=collections.Counter(); s=collections.Counter()
for line in open(sys.argv[1]):
    d=dict(kv.split(':',1) for kv in line.rstrip('\n').split('\t') if ':' in kv)
    u=d.get('uri','').split('?')[0]
    if not u.startswith('/api'): continue
    k=u if 'coordinate' in u or 'nearby' in u else 'other'
    n[k]+=1; s[k]+= float(d['reqtime'])>0.03
print(' '.join(f"{k}: {s[k]}/{n[k]} ({100*s[k]/n[k]:.1f}%>30ms)" for k in n))
