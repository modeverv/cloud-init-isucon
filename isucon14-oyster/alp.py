import re,collections,sys
cnt=collections.Counter(); tot=collections.defaultdict(float)
for line in open(sys.argv[1]):
    d=dict(kv.split(':',1) for kv in line.rstrip('\n').split('\t') if ':' in kv)
    if 'uri' not in d: continue
    uri=re.sub(r'/rides/[0-9A-Z]{26}/','/rides/:id/',d['uri'].split('?')[0])
    if not uri.startswith('/api'): uri='static'
    k=d['method']+' '+uri; cnt[k]+=1; tot[k]+=float(d['reqtime'])
for k,c in cnt.most_common(8): print(f"{c:8d} avg={tot[k]/c*1000:7.1f}ms {k}")
