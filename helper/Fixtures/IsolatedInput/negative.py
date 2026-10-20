#!/usr/bin/env python3
"""Opt-in fixture refusals and cancellation. Never replays a mutation."""
import argparse, json, threading, time
from pathlib import Path
from acceptance import Bridge, Rejected, private, read, until

def main():
    ap=argparse.ArgumentParser();ap.add_argument('--state',type=Path,required=True);ap.add_argument('--executable',required=True)
    ap.add_argument('--transport',choices=('helper','mcp'),default='helper');ap.add_argument('--go',action='store_true',required=True)
    a=ap.parse_args();root=a.state;out=root/'runs'/('negative-'+str(time.time_ns()));out.mkdir(mode=0o700)
    target=lambda:read(root/'target.json');results=[]
    assert target()['frontmost']!=target()['pid'],'target must stay in the background'
    b=Bridge(a.executable,a.transport,out)
    bundle='org.atenea.cua-fixture.target'
    if a.transport=='helper':base={'pid':target()['pid'],'bundle_id':bundle,'app':'Atenea target','visual_feedback':True}
    else:base={'repository':'atenea','application':bundle}
    def shot():return b.call('screenshot',base)
    def position(s,p):
        point=target()['points'][p]
        return {'x':(point['x']-s['window_origin_x'])*s['width']/s['window_width'],
                'y':(point['y']-s['window_origin_y'])*s['height']/s['window_height']}
    def button(name):
        s=shot()
        try:return b.call('click',{**base,'frame_id':s['frame_id'],**position(s,name)})
        except Rejected as e:
            if name not in ('second','overlap') or 'AXPress was not confirmed' not in str(e.payload):raise
            assert until(lambda:target()['key_window_id']==target()['second_window_id'] and
                (name!='overlap' or target()['second_frame']==target()['window_frame'])),'setup effect not observed; no replay'
            assert target()['frontmost']!=target()['pid'],'target activated; stop'
            results.append({'case':name+'-setup','uncertain_action_observed':True,'error':e.payload,'no_replay':True})
    def refused(label,args,contains):
        before=target()
        try:b.call('type',args);raise AssertionError(label+' unexpectedly dispatched')
        except Rejected as e:
            assert contains in str(e.payload).lower(),e.payload
            time.sleep(.04);after=target()
            assert all(after[k]==before[k] for k in ('marked_events','text','other_text','secure_length'))
            results.append({'case':label,'refused':True,'no_input_observed':True,'error':e.payload})
    try:
        refused('missing-frame',{**base,'text':'synthetic'},'frame')
        refused('unknown-frame',{**base,'frame_id':'not-a-capture','text':'synthetic'},'frame')
        s=shot();other=dict(base)
        if a.transport=='helper':
            other.update(pid=read(root/'decoy.json')['pid'],bundle_id='org.atenea.cua-fixture.decoy',app='Atenea decoy')
        else:other['application']='org.atenea.cua-fixture.decoy'
        beforeOther=read(root/'decoy.json')['marked_events']
        refused('frame-bound-to-other-pid',{**other,'frame_id':s['frame_id'],'text':'synthetic'},'frame')
        assert read(root/'decoy.json')['marked_events']==beforeOther

        if a.transport=='helper':
            s=shot();start=position(s,'dragStart');end=position(s,'dragEnd');offset=len(target()['marked_mouse'])
            args={**base,'frame_id':s['frame_id'],'from_x':start['x'],'from_y':start['y'],'to_x':end['x'],'to_y':end['y']}
            response={};request=b.n+1
            def drag():
                try:response['receipt']=b.call('drag',args)
                except Rejected as e:response['error']=e.payload
            worker=threading.Thread(target=drag);worker.start()
            assert until(lambda:len([p for p in target()['marked_mouse'][offset:] if p[0]==6])>=2,5),'drag did not reach fixture'
            b.notify('$/cancelRequest',{'id':request});worker.join(timeout=5);assert not worker.is_alive()
            assert response.get('error',{}).get('kind')=='canceled',response
            assert until(lambda:any(p[0]==2 for p in target()['marked_mouse'][offset:]))
            mouse=target()['marked_mouse'][offset:];path=[p for p in mouse if p[0]==6];releases=[p for p in mouse if p[0]==2]
            assert 2<=len(path)<18 and len(releases)==1 and releases[0][1:]==path[-1][1:] and releases[0][1]<440
            numbers=target()['marked_mouse_numbers'][offset:]
            assert len(numbers)==len(mouse) and len(set(numbers))==1,'drag release must match its down event number'
            results.append({'case':'canceled-drag','last_dispatched_release_confirmed':True,'no_destination_jump':True,'events':mouse,'error':response['error']})

        button('secure');s=shot()
        refused('secure-field',{**base,'frame_id':s['frame_id'],'text':'synthetic should be refused'},'secure')
        button('editor');s=shot()
        b.call('click',{**base,'frame_id':s['frame_id'],**position(s,'move')})
        refused('moved-window',{**base,'frame_id':s['frame_id'],'text':'synthetic'},'moved')
        button('second');s=shot()
        refused('different-focused-window',{**base,'frame_id':s['frame_id'],'text':'synthetic'},'window')
        button('overlap');s=shot()
        refused('ambiguous-same-pid-windows',{**base,'frame_id':s['frame_id'],'text':'synthetic'},'window')
    except Exception as e:
        results.append({'case':'aborted','error':e.payload if isinstance(e,Rejected) else str(e),'no_replay':True})
        raise
    finally:
        private(out/'result.json',{'transport':a.transport,'results':results});b.close()
        print('PRIVATE_RESULT '+str(out/'result.json'),flush=True)
    print(json.dumps({'negative_cases_passed':len(results)}),flush=True)
if __name__=='__main__':main()
