import {digest,metric,page,report} from "./ui-fixture";
export const currentDigest="d".repeat(64);
export const currentMetrics={...metric,MRRAt10:0.9};
export const delta={RecallAt20:0,HitAt5:0,MRRAt10:-0.1,NDCGAt10:0};
export const comparisonPage={items:[{...page.items[0],model_label:"基线配置"},{...page.items[0],digest:currentDigest,model_label:"当前配置",summary:{...report.summary,Metrics:currentMetrics}}],next_cursor:null};
export function comparison(offset=0,reverse=false){
  const before=report.rows;
  const after=report.rows.map(row=>({...row,metrics:row.metrics===null?null:currentMetrics,ranked:[],timings_ms:{total:3}}));
  return {
    baseline_digest:reverse?currentDigest:digest,current_digest:reverse?digest:currentDigest,gate:reverse?"passed":"regressed",
    baseline:reverse?{...report.summary,Metrics:currentMetrics}:report.summary,current:reverse?report.summary:{...report.summary,Metrics:currentMetrics},
    delta:{...delta,MRRAt10:reverse?0.1:-0.1},total_rows:21,next_offset:offset+20<21?offset+20:null,
    rows:before.slice(offset,offset+20).map((row,index)=>({id:row.id,baseline:reverse?after[offset+index]:row,current:reverse?row:after[offset+index],delta:row.metrics===null?null:{...delta,MRRAt10:reverse?0.1:-0.1},evidence_changed:true})),
  };
}
