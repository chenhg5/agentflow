#!/usr/bin/env node
'use strict';
const fs = require('node:fs');
if (process.env.FARM_SITE !== 'orchard-2') {
  throw new Error('FARM_SITE must be orchard-2');
}
const data = JSON.parse(fs.readFileSync(process.env.AGENTFLOW_INPUT_FILE, 'utf8'));
if (!data || typeof data.batch !== 'string' || !Array.isArray(data.readings) ||
    !data.readings.every(x => typeof x === 'number' && Number.isFinite(x)) ||
    !data.meta || Array.isArray(data.meta) || typeof data.meta !== 'object' ||
    typeof data.meta.unit !== 'string' || typeof data.meta.valid !== 'boolean') {
  throw new Error('Invalid batch/readings/meta JSON types');
}
const result = {batch: data.batch, total: data.readings.reduce((a, b) => a + b, 0),
                unit: data.meta.unit, valid: data.meta.valid};
fs.writeFileSync(process.env.AGENTFLOW_RESULT_FILE, JSON.stringify(result));
