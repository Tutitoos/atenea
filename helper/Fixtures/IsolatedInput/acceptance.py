#!/usr/bin/env python3
"""Opt-in local fixture acceptance. Inputs use only the supplied helper or Atenea MCP.
No replay: a refused/canceled/uncertain mutation is recorded, never retried.
"""
import argparse, base64, datetime, json, os, queue, subprocess, threading, time
from pathlib import Path

class Rejected(Exception):
    def __init__(self, payload): self.payload=payload; super().__init__(payload.get('kind','refused'))

def private(path, value):
    data=value if isinstance(value,bytes) else (json.dumps(value,indent=2)+'\n').encode()
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'wb') as f:f.write(data)

def read(path): return json.loads(path.read_text())
def until(test, timeout=2):
    end=time.monotonic()+timeout
    while time.monotonic()<end:
        if test():return True
        time.sleep(.01)
    return False

class Bridge:
    def __init__(self, executable, transport, output):
        self.transport=transport;self.n=0;self.q=queue.Queue()
        self.p=subprocess.Popen([executable]+(['mcp'] if transport=='mcp' else []),stdin=subprocess.PIPE,stdout=subprocess.PIPE,
            stderr=open(output/'bridge.stderr','x'),text=True,bufsize=1)
        os.chmod(output/'bridge.stderr',0o600)
        threading.Thread(target=lambda:[self.q.put(json.loads(s)) for s in self.p.stdout],daemon=True).start()
        self.request('initialize',{'protocolVersion':'2025-06-18','clientInfo':{'name':'codex','version':'1'},'capabilities':{}})
        self.notify('notifications/initialized',{})
    def notify(self,method,params):
        self.p.stdin.write(json.dumps({'jsonrpc':'2.0','method':method,'params':params})+'\n');self.p.stdin.flush()
    def request(self,method,params):
        self.n+=1
        self.p.stdin.write(json.dumps({'jsonrpc':'2.0','id':self.n,'method':method,'params':params})+'\n');self.p.stdin.flush()
        reply=self.q.get(timeout=25)
        assert reply.get('id')==self.n
        if 'error' in reply:raise Rejected({'kind':'protocol','error':reply['error'].get('message','')})
        return reply['result']
    def call(self,tool,args):
        if self.transport=='mcp':tool='desktop.'+tool
        result=self.request('tools/call',{'name':tool,'arguments':args})
        payload=result.get('structuredContent')
        if not isinstance(payload,dict):
            payload={}
            for c in result.get('content',[]):
                if c.get('type')!='text':continue
                try:parsed=json.loads(c['text'])
                except (ValueError,TypeError):continue
                if isinstance(parsed,dict):payload=parsed;break
        if result.get('isError'):
            payload=dict(payload)
            if 'kind' not in payload and 'error_code' in payload:payload['kind']=payload['error_code']
            payload.setdefault('error','\n'.join(c['text'] for c in result.get('content',[]) if c.get('type')=='text'))
            raise Rejected(payload)
        return payload
    def close(self):
        self.p.stdin.close();self.p.wait(timeout=5)

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--state',type=Path,required=True);ap.add_argument('--executable',required=True)
    ap.add_argument('--transport',choices=('helper','mcp'),default='helper');ap.add_argument('--foreground',choices=('target','decoy','keep'),required=True)
    ap.add_argument('--go',action='store_true',required=True);a=ap.parse_args()
    root=a.state;run=root/'runs'/(datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%S')+'-'+str(time.time_ns()))
    run.mkdir(parents=True,mode=0o700)
    target=lambda:read(root/'target.json');decoy=lambda:read(root/'decoy.json')
    if a.foreground!='keep':
        subprocess.run(['open',str(root/(a.foreground+'.app'))],check=True)
        assert until(lambda:read(root/(a.foreground+'.json'))['frontmost']==read(root/(a.foreground+'.json'))['pid'])
    else:assert target()['frontmost']!=target()['pid'],'target must be in the background; no input sent'
    observer=subprocess.Popen([str(root/'observer'),str(root)],stdout=subprocess.DEVNULL,stderr=open(run/'observer.stderr','x'))
    os.chmod(run/'observer.stderr',0o600)
    bridge=Bridge(a.executable,a.transport,run);history=[];matrix={}
    app='org.atenea.cua-fixture.target'
    if a.transport=='helper':
        identity=next(x for x in bridge.call('list_apps',{})['apps'] if x.get('bundle_id')==app)
        base={'pid':identity['pid'],'bundle_id':app,'app':identity['name'],'visual_feedback':True}
    else:base={'repository':'atenea','application':app}
    # Settle setup activation and the user's final movement before sampling.
    previous=None;quiet=time.monotonic()
    def settled():
        nonlocal previous,quiet
        current=(target()['pointer'],target()['frontmost'],decoy()['events'])
        if current!=previous:previous=current;quiet=time.monotonic()
        return time.monotonic()-quiet>.5
    if a.foreground!='keep':assert until(settled,5),'setup did not settle; no input sent'
    def snapshot(label):
        shot=bridge.call('screenshot',base)
        private(run/(label+'.png'),base64.b64decode(shot['png_base64']))
        return shot
    def action(name,label,point=None,**fields):
        shot=snapshot(label+'-before');payload={**base,'frame_id':shot['frame_id'],**fields}
        def xy(p):return {'x':(p['x']-shot['window_origin_x'])*shot['width']/shot['window_width'],
                         'y':(p['y']-shot['window_origin_y'])*shot['height']/shot['window_height']}
        if point:payload.update(xy(target()['points'][point]))
        if name=='drag':
            e=xy(target()['points']['dragEnd']);payload.update(from_x=payload.pop('x'),from_y=payload.pop('y'),to_x=e['x'],to_y=e['y'])
        phase=label+'-'+str(time.time_ns());(root/'phase.json').write_text(json.dumps({'phase':phase}))
        assert until(lambda:(root/'observer.json').exists() and read(root/'observer.json')['phase']==phase)
        before=target();other=decoy();started=time.monotonic()
        row={'tool':name,'label':label,'request_id':bridge.n+1,'arguments':{k:v for k,v in payload.items() if k!='text'}}
        history.append(row)
        try:row['receipt']=bridge.call(name,payload)
        except Rejected as e:
            row['error']=e.payload
            if e.payload.get('kind')=='canceled' or 'human input' in json.dumps(e.payload).lower():
                row['human_pause']=True
                raise
        row['elapsed_ms']=(time.monotonic()-started)*1000
        assert until(lambda:read(root/'observer.json')['phase']==phase and read(root/'observer.json')['samples']>30)
        time.sleep(.04)
        sensor=read(root/'observer.json');row['observer']=sensor
        row['decoy_unchanged']=all(decoy()[k]==other[k] for k in ('events','count','text','scroll_y','drag_path','releases'))
        row['isolation_measured']=sensor['focus_readable'] and sensor['trusted'] and sensor['max_sample_gap_ms']<100 and all(sensor[k]==0 for k in
            ('pointer_changes','frontmost_changes','focus_changes','state_changes','activation_notifications')) and row['decoy_unchanged']
        if a.foreground=='keep':
            row['decoy_no_cua_events']=decoy()['marked_events']==other['marked_events']
            row['human_activity_observed']=sum(sensor['human_destination_events'].values())>0
            row['isolation_measured']=sensor['trusted'] and sensor['taps_active'] and sensor['global_marker_events']==0 and sensor['target_activations']==0 and row['decoy_no_cua_events']
        return before,row
    def check(label,row,predicate):
        row['effect_confirmed']=until(predicate)
        matrix[label]={'effect_confirmed':row['effect_confirmed'],'isolation_measured':row['isolation_measured'],'refused':'error' in row}
        print(json.dumps({'case':label,**matrix[label]},ensure_ascii=False),flush=True)
        if a.foreground=='keep' and row['observer']['target_activations']:
            raise Rejected({'kind':'focus_changed','error':'target activated; observe before further input'})
    try:
        b,r=action('click','click','increment');check('click',r,lambda:target()['count']==b['count']+1)
        b,r=action('click','double-click','canvas',clicks=2);check('double-click',r,lambda:target()['downs']==b['downs']+2)
        b,r=action('move','move','canvas')
        if a.transport=='helper':
            h=bridge.call('health',{});p=target()['points']['canvas']
            r['virtual_pointer_confirmed']=h.get('virtual_pointer_pid')==b['pid'] and abs(h.get('virtual_pointer_x',-1)-p['x'])<.1 and abs(h.get('virtual_pointer_y',-1)-p['y'])<.1
        else:
            h=r.get('receipt',{}).get('input',{});p=target()['points']['canvas']
            r['virtual_pointer_confirmed']=abs(h.get('virtual_pointer_x',-1)-p['x'])<.1 and abs(h.get('virtual_pointer_y',-1)-p['y'])<.1
        r['hover_effect']=target()['moves']>b['moves']
        check('move',r,lambda:r['virtual_pointer_confirmed'])
        b,r=action('drag','drag','dragStart');check('drag',r,lambda:len(target()['drag_path'])==18 and len(target()['releases'])==len(b['releases'])+1 and abs(target()['releases'][-1][0]-440)<2)
        b,r=action('scroll','scroll','scroll',dx=0,dy=-120);check('scroll',r,lambda:target()['scroll_y']!=b['scroll_y'])
        b,r=action('type','type',text='isolated lowercase ñ 123');check('type',r,lambda:'isolated lowercase ñ 123' in target()['text'])
        b,r=action('key','select-all',key='a',modifiers=['cmd'])
        check('select-all',r,lambda:target()['selection_length']==len(target()['text']))
        b,r=action('type','replace-selection',text='SECOND isolated ñ 123');check('replace-selection',r,lambda:target()['text']=='SECOND isolated ñ 123')
        b,r=action('key','shift-left',key='left',modifiers=['shift'])
        check('shift-left',r,lambda:target()['selection_length']==1)
        b,r=action('type','replace-character',text='Z')
        check('replace-character',r,lambda:target()['text']==b['text'][:-1]+'Z')
    except Rejected as e:
        history.append({'phase':'aborted','error':e.payload,'no_replay':True})
        raise
    finally:
        private(run/'result.json',{'foreground':a.foreground,'transport':a.transport,'matrix':matrix,'attempts':history,
            'hover_requires_separate_app_acceptance':True,'no_automatic_replay':True})
        bridge.close();observer.terminate();observer.wait(timeout=5)
        print('PRIVATE_RESULT '+str(run/'result.json'),flush=True)
    if not all(v['effect_confirmed'] and v['isolation_measured'] and not v['refused'] for v in matrix.values()):
        raise SystemExit(1)
if __name__=='__main__':main()
