import {comparison} from "./comparison-ui-fixture";
import {historyItems} from "./run-history-fixture";
export const runComparisonPage={items:[{...historyItems[10],id:1},{...historyItems[10],id:2},{...historyItems[0],id:3}],next_cursor:null};
export function runComparison(baseline=1,current=2,offset=0){return {baseline_run_id:baseline,current_run_id:current,source:"executed",comparison:comparison(offset,baseline===2)};}
