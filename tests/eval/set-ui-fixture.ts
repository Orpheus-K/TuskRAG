import type {StoredEvalSet} from "../../packages/api-client/src/index";
export const savedSet:StoredEvalSet={id:9,revision:1,digest:"a".repeat(64),created_at:"2026-11-06T00:00:00Z",definition:{name:"验收题集",cases:[{id:"q1",question:"申请条件？",answerable:true,expected:[{document_id:3,section_id:4,page:2}],reference_answer:"按制度申请",tags:["精确词"]},{id:"q2",question:"未记载内容？",answerable:false,expected:[],tags:[]}]}};
export const setPage={items:[{id:9,revision:1,digest:savedSet.digest,created_at:savedSet.created_at,name:savedSet.definition.name,case_count:2}],next_cursor:null};
