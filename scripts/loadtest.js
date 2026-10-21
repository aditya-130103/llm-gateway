import http from 'k6/http';
import { check, sleep } from 'k6';

export const options = {
  stages: [
    { duration: '30s', target: 5 },   // ramp up
    { duration: '1m',  target: 5 },   // steady state
    { duration: '10s', target: 0 },   // ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<3000'],
    http_req_failed:   ['rate<0.05'],
  },
};

const GATEWAY_URL = __ENV.GATEWAY_URL || 'http://localhost:8030';
const TEAM_TOKEN  = __ENV.TEAM_TOKEN  || 'your-team-token';

export default function () {
  const res = http.post(
    `${GATEWAY_URL}/v1/chat/completions`,
    JSON.stringify({
      model: 'gpt-4.1-mini',
      messages: [{ role: 'user', content: 'What is 2+2?' }],
    }),
    {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${TEAM_TOKEN}`,
      },
    }
  );

  check(res, {
    'status 200':  (r) => r.status === 200,
    'has choices': (r) => JSON.parse(r.body).choices !== undefined,
  });

  sleep(0.1);
}
