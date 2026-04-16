// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

const Koa = require('koa');
const Router = require('@koa/router');
const lorem = require('lorem-ipsum');

const app = new Koa();
const router = new Router();

// Log requests
app.use(async (ctx, next) => {
  const start = new Date();
  await next();
  const ms = new Date() - start;
  console.log('%s %s - %s', ctx.method, ctx.url, ms);
});

router.get('/api/lorem-ipsum', (ctx) => {
  ctx.body = {
    body: lorem({
      count: 10,
      units: 'paragraphs'
    })
  };
});

router.get('/api/health-check', (ctx) => {
  ctx.body = 'Ready';
});

app.use(router.routes());
app.use(router.allowedMethods());

app.listen(3000);

console.log('Application ready and running...');
