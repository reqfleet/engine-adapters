import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  thresholds: { http_req_failed: ['rate<0.01'] },
};

export default function () {
  const response = http.get('https://test.k6.io/');
  check(response, { 'HTTP 200': (r) => r.status === 200 });
  sleep(1);
}
