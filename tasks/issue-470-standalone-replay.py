#!/usr/bin/env python3
"""Full replay with PROPER submit CLI usage."""
import json, subprocess, re, sys

GOX_PORT = 62928
SA = "rippled-standalone"

def gox(method, params=None):
    body = {"method": method, "params": [params or {}]}
    r = subprocess.run(['curl','-s','-X','POST',f'http://127.0.0.1:{GOX_PORT}','-H','Content-Type: application/json','-d',json.dumps(body)],capture_output=True,text=True)
    try: return json.loads(r.stdout).get('result',{})
    except: return {}

def admin(*args):
    """Run rippled CLI inside container. args are positional CLI args."""
    cmd = ['docker','exec',SA,'/opt/ripple/bin/rippled','--conf','/etc/opt/ripple/rippled.cfg'] + list(args)
    r = subprocess.run(cmd,capture_output=True,text=True,timeout=30)
    m = re.search(r'^\{', r.stdout, re.MULTILINE)
    if m:
        try: return json.loads(r.stdout[m.start():])
        except Exception as e: return {'parse_err':str(e),'raw':r.stdout[m.start():m.start()+300]}
    return {'raw': r.stdout[:300]}

def submit(blob):
    return admin('submit', blob)

def info():
    return admin('server_info').get('result',{}).get('info',{})

# Advance standalone (skipping 0-tx ledgers) to seq 6
while True:
    i = info()
    v = i.get('validated_ledger',{}).get('seq',0)
    if v >= 6: break
    admin('ledger_accept')
print(f'standalone validated seq = {v}')

# Replay seqs 7-11 in TransactionIndex order
for seq in [7,8,9,10,11]:
    L = gox('ledger',{'ledger_index':seq,'transactions':True,'expand':True,'binary':True})
    txs_b = L.get('ledger',{}).get('transactions',[])
    L2 = gox('ledger',{'ledger_index':seq,'transactions':True,'expand':True})
    indices = {t['hash']: t['meta'].get('TransactionIndex',0) for t in L2.get('ledger',{}).get('transactions',[])}
    txs_b.sort(key=lambda t: indices.get(t['hash'],0))
    print(f'\n=== seq {seq}: {len(txs_b)} txs ===')
    for t in txs_b:
        r = submit(t.get('tx_blob',''))
        res = r.get('result',{}).get('engine_result','?')
        msg = r.get('result',{}).get('engine_result_message','')[:50]
        ti = indices.get(t['hash'],'?')
        print(f'  ti={ti} {t["hash"][:12]} → {res} {msg}')
    admin('ledger_accept')
    v = info().get('validated_ledger',{}).get('seq')
    print(f'  standalone validated seq = {v}')

# Compare seq 11
print('\n=== compare seq 11 ===')
g = gox('ledger',{'ledger_index':11}).get('ledger',{})
s = admin('ledger', '11', 'false').get('result',{}).get('ledger',{})
for k in ['ledger_hash','account_hash','transaction_hash','close_time','total_coins']:
    g_v = g.get(k,'?')
    s_v = s.get(k,'?')
    match = '✓' if str(g_v)==str(s_v) else '✗'
    print(f'  {match} {k}: gox={str(g_v)[:32]} sa={str(s_v)[:32]}')
