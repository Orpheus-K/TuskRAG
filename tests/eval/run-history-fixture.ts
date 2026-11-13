import type {EvalRunStatus} from "../../packages/api-client/src/index";
// Transport fixtures only; not model quality measurements.
export const historyItems: EvalRunStatus[]=Array.from({length:11},(_,i)=>({
 id:i+1,job_id:i+101,set:{set_id:9,revision:2},mode:"rrf",status:i===10?"succeeded":i===2?"failed":i===3?"cancelled":"queued",attempts:i===10?1:0,max_attempts:3,failure_code:i===2?"permission_revoked":null,result_available:i===10,created_at:"2026-11-06T00:00:00Z",finished_at:[2,3,10].includes(i)?"2026-11-06T00:01:00Z":null
}));
export const historyPage=(cursor:number)=>({items:historyItems.filter(i=>i.id>cursor).slice(0,10),next_cursor:cursor===0?10:null});
