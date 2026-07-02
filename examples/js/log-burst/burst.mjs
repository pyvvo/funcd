// src/burst.ts
function handle(_ctx, event) {
  const items = Math.max(100, event.data?.items ?? 100);
  const batch = event.data?.batch ?? "default";
  let emitted = 0;
  for (let i = 0; i < items; i++) {
    console.log("processing item", { i, batch });
    emitted++;
    if (i % 10 === 0) {
      console.warn("slow item", { i, batch, latencyMs: 120 });
      emitted++;
    }
    if (i % 25 === 0) {
      console.error("item failed", { i, batch, reason: "simulated" });
      emitted++;
    }
  }
  console.log("burst complete", { batch, emitted });
  emitted++;
  return { emitted };
}

// .burst.validator.mjs
var _funcdInput = validate10;
function validate10(data, { instancePath = "", parentData, parentDataProperty, rootData = data } = {}) {
  ;
  let vErrors = null;
  let errors = 0;
  if (data && typeof data == "object" && !Array.isArray(data)) {
    for (const key0 in data) {
      if (!(key0 === "items" || key0 === "batch")) {
        const err0 = { instancePath, schemaPath: "#/additionalProperties", keyword: "additionalProperties", params: { additionalProperty: key0 }, message: "must NOT have additional properties" };
        if (vErrors === null) {
          vErrors = [err0];
        } else {
          vErrors.push(err0);
        }
        errors++;
      }
    }
    if (data.items !== void 0) {
      let data0 = data.items;
      if (!(typeof data0 == "number" && isFinite(data0))) {
        const err1 = { instancePath: instancePath + "/items", schemaPath: "#/properties/items/type", keyword: "type", params: { type: "number" }, message: "must be number" };
        if (vErrors === null) {
          vErrors = [err1];
        } else {
          vErrors.push(err1);
        }
        errors++;
      }
    }
    if (data.batch !== void 0) {
      if (typeof data.batch !== "string") {
        const err2 = { instancePath: instancePath + "/batch", schemaPath: "#/properties/batch/type", keyword: "type", params: { type: "string" }, message: "must be string" };
        if (vErrors === null) {
          vErrors = [err2];
        } else {
          vErrors.push(err2);
        }
        errors++;
      }
    }
  } else {
    const err3 = { instancePath, schemaPath: "#/type", keyword: "type", params: { type: "object" }, message: "must be object" };
    if (vErrors === null) {
      vErrors = [err3];
    } else {
      vErrors.push(err3);
    }
    errors++;
  }
  validate10.errors = vErrors;
  return errors === 0;
}
var _funcdOutput = validate11;
function validate11(data, { instancePath = "", parentData, parentDataProperty, rootData = data } = {}) {
  ;
  let vErrors = null;
  let errors = 0;
  if (data && typeof data == "object" && !Array.isArray(data)) {
    if (data.emitted === void 0) {
      const err0 = { instancePath, schemaPath: "#/required", keyword: "required", params: { missingProperty: "emitted" }, message: "must have required property 'emitted'" };
      if (vErrors === null) {
        vErrors = [err0];
      } else {
        vErrors.push(err0);
      }
      errors++;
    }
    for (const key0 in data) {
      if (!(key0 === "emitted")) {
        const err1 = { instancePath, schemaPath: "#/additionalProperties", keyword: "additionalProperties", params: { additionalProperty: key0 }, message: "must NOT have additional properties" };
        if (vErrors === null) {
          vErrors = [err1];
        } else {
          vErrors.push(err1);
        }
        errors++;
      }
    }
    if (data.emitted !== void 0) {
      let data0 = data.emitted;
      if (!(typeof data0 == "number" && isFinite(data0))) {
        const err2 = { instancePath: instancePath + "/emitted", schemaPath: "#/properties/emitted/type", keyword: "type", params: { type: "number" }, message: "must be number" };
        if (vErrors === null) {
          vErrors = [err2];
        } else {
          vErrors.push(err2);
        }
        errors++;
      }
    }
  } else {
    const err3 = { instancePath, schemaPath: "#/type", keyword: "type", params: { type: "object" }, message: "must be object" };
    if (vErrors === null) {
      vErrors = [err3];
    } else {
      vErrors.push(err3);
    }
    errors++;
  }
  validate11.errors = vErrors;
  return errors === 0;
}
function __funcdValidateInput(d) {
  return _funcdInput(d) ? [] : _funcdInput.errors ?? [];
}
function __funcdValidateOutput(d) {
  return _funcdOutput(d) ? [] : _funcdOutput.errors ?? [];
}
export {
  __funcdValidateInput,
  __funcdValidateOutput,
  _funcdInput,
  _funcdOutput,
  handle
};
