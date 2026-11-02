import http from 'k6/http';
import { sleep } from 'k6';

// Upload targets.csv with this plan. Use an absolute path: the adapter generates
// its entry module in another directory. Consume the supplied partition whole.
// This example uses one URL per line, not a general quoted CSV parser.
const targets = open('/test-data/targets.csv').trim().split('\n');

export default function () {
  http.get(targets[__ITER % targets.length].trim());
  sleep(1);
}
