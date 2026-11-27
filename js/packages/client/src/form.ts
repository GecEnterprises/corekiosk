import type { FormField } from "./generated/types.js";

export type FormErrors = Record<string, string>;

export function validateFormFields(fields: FormField[], values: Record<string, unknown>): FormErrors {
  const errors: FormErrors = {};
  for (const field of fields) {
    const raw = values[field.key];
    if (field.type === "bool") {
      if (field.required && typeof raw !== "boolean") {
        errors[field.key] = `${field.label} is required`;
      }
      continue;
    }
    if (field.type === "number") {
      if (raw === undefined || raw === null || raw === "") {
        if (field.required) errors[field.key] = `${field.label} is required`;
        continue;
      }
      const num = Number(raw);
      if (Number.isNaN(num)) {
        errors[field.key] = `${field.label} must be a number`;
        continue;
      }
      if (field.min !== undefined && field.min !== null && num < field.min) errors[field.key] = `Minimum is ${field.min}`;
      if (field.max !== undefined && field.max !== null && num > field.max) errors[field.key] = `Maximum is ${field.max}`;
      continue;
    }
    let str = typeof raw === "string" ? raw.trim() : "";
    if (field.type === "handle" && field.prefix && str.startsWith(field.prefix)) {
      str = str.slice(field.prefix.length);
    }
    if (!str) {
      if (field.required) errors[field.key] = `${field.label} is required`;
      continue;
    }
    if (field.min_len && str.length < field.min_len) {
      errors[field.key] = `Must be at least ${field.min_len} characters`;
      continue;
    }
    if (field.max_len && str.length > field.max_len) {
      errors[field.key] = `Must be at most ${field.max_len} characters`;
      continue;
    }
    if (field.type === "email") {
      const at = str.indexOf("@");
      if (at <= 0 || at === str.length - 1 || !str.slice(at + 1).includes(".") || /\s/.test(str)) {
        errors[field.key] = "Enter a valid email address";
      }
    }
    if (field.type === "enum" && field.options?.length) {
      if (!field.options.some((o) => o.value === str)) {
        errors[field.key] = "Invalid selection";
      }
    }
  }
  return errors;
}
