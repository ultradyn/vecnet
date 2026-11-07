import { readFileSync } from 'node:fs';

// Published send schema. attachmentReference forbids server-owned proof fields.
const document = JSON.parse(readFileSync(new URL('../../../internal/envelope/schema.json', import.meta.url), 'utf8')) as {
  $defs: Record<string, SchemaNode>;
};

interface SchemaNode {
  $ref?: string;
  type?: string | string[];
  const?: unknown;
  enum?: unknown[];
  properties?: Record<string, SchemaNode>;
  required?: string[];
  additionalProperties?: boolean;
  not?: SchemaNode;
  anyOf?: SchemaNode[];
  allOf?: SchemaNode[];
  if?: SchemaNode;
  then?: SchemaNode;
  else?: SchemaNode;
  pattern?: string;
  minimum?: number;
  maximum?: number;
  minLength?: number;
  maxLength?: number;
}

function resolve(schema: SchemaNode): SchemaNode {
  if (!schema.$ref) return schema;
  const name = schema.$ref.slice('#/$defs/'.length);
  const target = document.$defs[name];
  if (!target) return schema;
  return { ...target, ...schema, $ref: undefined };
}

function typeOk(types: string[], value: unknown): boolean {
  return types.some(kind => {
    if (kind === 'object') return value !== null && typeof value === 'object' && !Array.isArray(value);
    if (kind === 'array') return Array.isArray(value);
    if (kind === 'integer') return typeof value === 'number' && Number.isInteger(value);
    if (kind === 'number') return typeof value === 'number' && Number.isFinite(value);
    if (kind === 'null') return value === null;
    return typeof value === kind;
  });
}

function check(schema: SchemaNode, value: unknown, path: string): string[] {
  const node = resolve(schema);
  const errors: string[] = [];
  if (node.allOf) for (const branch of node.allOf) errors.push(...check(branch, value, path));
  if (node.anyOf && !node.anyOf.some(branch => check(branch, value, path).length === 0)) errors.push(`${path || '/'}: anyOf`);
  if (node.not && check(node.not, value, path).length === 0) errors.push(`${path || '/'}: not`);
  if (node.if) {
    const branch = check(node.if, value, path).length === 0 ? node.then : node.else;
    if (branch) errors.push(...check(branch, value, path));
  }
  if (Object.hasOwn(node, 'const') && !Object.is(value, node.const)) errors.push(`${path || '/'}: const`);
  if (node.enum && !node.enum.some(item => Object.is(item, value))) errors.push(`${path || '/'}: enum`);
  if (node.type) {
    const types = typeof node.type === 'string' ? [node.type] : node.type;
    if (!typeOk(types, value)) {
      errors.push(`${path || '/'}: type`);
      return errors;
    }
  }
  if (typeof value === 'string') {
    if (node.minLength !== undefined && [...value].length < node.minLength) errors.push(`${path}: minLength`);
    if (node.maxLength !== undefined && [...value].length > node.maxLength) errors.push(`${path}: maxLength`);
    if (node.pattern && !new RegExp(node.pattern).test(value)) errors.push(`${path}: pattern`);
  }
  if (typeof value === 'number') {
    if (node.minimum !== undefined && value < node.minimum) errors.push(`${path}: minimum`);
    if (node.maximum !== undefined && value > node.maximum) errors.push(`${path}: maximum`);
  }
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    const record = value as Record<string, unknown>;
    for (const key of node.required ?? []) if (!Object.hasOwn(record, key)) errors.push(`${path}/${key}: required`);
    for (const [key, child] of Object.entries(record)) {
      const pointer = `${path}/${key}`;
      const field = node.properties?.[key];
      if (field) errors.push(...check(field, child, pointer));
      else if (node.additionalProperties === false) errors.push(`${pointer}: unknown key`);
    }
  }
  return errors;
}

/** Errors from the published attachmentReference schema. Empty means the send attachment is valid. */
export function attachmentReferenceErrors(value: unknown): string[] {
  const schema = document.$defs.attachmentReference;
  if (!schema?.not) throw new Error('published attachmentReference no longer forbids server-owned fields');
  return check(schema, value, '');
}
