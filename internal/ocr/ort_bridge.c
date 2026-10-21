//go:build !noocr

// ort_bridge.c — C wrappers for ONNX Runtime C API called from Go via CGO.
// Build constraint: paired with onnx_api.go (//go:build !noocr).
#include <onnxruntime_c_api.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// Helper: release status if non-null, and warn via stderr.
// Used for functions like SetSessionGraphOptimizationLevel that practically never fail
// but are marked warn_unused_result.
static void check_and_release(OrtStatus* st) {
    if (st) {
        fprintf(stderr, "ort_bridge: unexpected status from ORT call: %s\n",
                OrtGetApiBase()->GetApi(21)->GetErrorMessage(st));
        OrtGetApiBase()->GetApi(21)->ReleaseStatus(st);
    }
}

typedef struct {
    OrtEnv* env;
    OrtSession* session;
    OrtMemoryInfo* memoryInfo;
} ort_session_t;

ort_session_t* ort_create_session(const void* modelData, size_t modelSize, char** errMsg) {
    const OrtApi* api = OrtGetApiBase()->GetApi(21);
    if (!api) {
        *errMsg = strdup("failed to get ONNX API");
        return NULL;
    }

    OrtEnv* env = NULL;
    OrtStatus* status = api->CreateEnv(3, "docutil", &env);
    if (status) {
        *errMsg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return NULL;
    }

    OrtSessionOptions* opts = NULL;
    status = api->CreateSessionOptions(&opts);
    if (status) {
        *errMsg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        api->ReleaseEnv(env);
        return NULL;
    }
    check_and_release(api->SetSessionGraphOptimizationLevel(opts, 1));
    check_and_release(api->SetIntraOpNumThreads(opts, 4));

    OrtSession* session = NULL;
    status = api->CreateSessionFromArray(env, modelData, modelSize, opts, &session);
    api->ReleaseSessionOptions(opts);
    if (status) {
        *errMsg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        api->ReleaseEnv(env);
        return NULL;
    }

    OrtMemoryInfo* memoryInfo = NULL;
    status = api->CreateCpuMemoryInfo(OrtArenaAllocator, OrtMemTypeDefault, &memoryInfo);
    if (status) {
        *errMsg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        api->ReleaseSession(session);
        api->ReleaseEnv(env);
        return NULL;
    }

    ort_session_t* s = (ort_session_t*)malloc(sizeof(ort_session_t));
    s->env = env;
    s->session = session;
    s->memoryInfo = memoryInfo;
    *errMsg = NULL;
    return s;
}

void ort_destroy_session(ort_session_t* s) {
    if (!s) return;
    const OrtApi* api = OrtGetApiBase()->GetApi(21);
    if (s->memoryInfo) api->ReleaseMemoryInfo(s->memoryInfo);
    if (s->session) api->ReleaseSession(s->session);
    if (s->env) api->ReleaseEnv(s->env);
    free(s);
}

char* ort_run(ort_session_t* s,
    const char* inputName, const float* inputData, const int64_t* inputShape, int inputDims,
    float** outData, int64_t** outShape, int* outDims) {

    const OrtApi* api = OrtGetApiBase()->GetApi(21);
    *outData = NULL;
    *outShape = NULL;
    *outDims = 0;

    size_t numElements = 1;
    int i;
    for (i = 0; i < inputDims; i++) numElements *= (size_t)inputShape[i];

    OrtValue* inputTensor = NULL;
    OrtStatus* status = api->CreateTensorWithDataAsOrtValue(
        s->memoryInfo, (void*)inputData, numElements * sizeof(float),
        inputShape, (size_t)inputDims, ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT, &inputTensor);
    if (status) {
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    OrtAllocator* allocator = NULL;
    status = api->GetAllocatorWithDefaultOptions(&allocator);
    if (status) {
        api->ReleaseValue(inputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    char* actualInputName = (char*)inputName;
    char* inputNameAlloc = NULL;
    if (!inputName) {
        char* name = NULL;
        status = api->SessionGetInputName(s->session, 0, allocator, &name);
        if (status) {
            api->ReleaseValue(inputTensor);
            char* msg = strdup(api->GetErrorMessage(status));
            api->ReleaseStatus(status);
            return msg;
        }
        inputNameAlloc = strdup(name);
        check_and_release(api->AllocatorFree(allocator, name));
        actualInputName = inputNameAlloc;
    }

    char* outputName = NULL;
    status = api->SessionGetOutputName(s->session, 0, allocator, &outputName);
    if (status) {
        free(inputNameAlloc);
        api->ReleaseValue(inputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    const char* inputNames[] = {actualInputName};
    const char* outputNames[] = {outputName};
    OrtValue* outputs[1] = {NULL};

    OrtRunOptions* runOptions = NULL;
    status = api->CreateRunOptions(&runOptions);
    if (status) {
        free(inputNameAlloc);
        check_and_release(api->AllocatorFree(allocator, outputName));
        api->ReleaseValue(inputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    status = api->Run(s->session, runOptions,
        inputNames, (const OrtValue* const*)&inputTensor, 1,
        outputNames, 1, outputs);

    api->ReleaseRunOptions(runOptions);
    api->ReleaseValue(inputTensor);
    free(inputNameAlloc);
    check_and_release(api->AllocatorFree(allocator, outputName));
    if (status) {
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    OrtValue* outputTensor = outputs[0];
    OrtTensorTypeAndShapeInfo* shapeInfo = NULL;
    status = api->GetTensorTypeAndShape(outputTensor, &shapeInfo);
    if (status) {
        api->ReleaseValue(outputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    size_t numDims = 0;
    status = api->GetDimensionsCount(shapeInfo, &numDims);
    if (status) {
        api->ReleaseTensorTypeAndShapeInfo(shapeInfo);
        api->ReleaseValue(outputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    int64_t* dims = (int64_t*)malloc(numDims * sizeof(int64_t));
    status = api->GetDimensions(shapeInfo, dims, numDims);
    if (status) {
        free(dims);
        api->ReleaseTensorTypeAndShapeInfo(shapeInfo);
        api->ReleaseValue(outputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    size_t totalElements = 1;
    size_t j;
    for (j = 0; j < numDims; j++) totalElements *= (size_t)dims[j];

    float* rawData = NULL;
    status = api->GetTensorMutableData(outputTensor, (void**)&rawData);
    if (status) {
        free(dims);
        api->ReleaseTensorTypeAndShapeInfo(shapeInfo);
        api->ReleaseValue(outputTensor);
        char* msg = strdup(api->GetErrorMessage(status));
        api->ReleaseStatus(status);
        return msg;
    }

    float* data = (float*)malloc(totalElements * sizeof(float));
    memcpy(data, rawData, totalElements * sizeof(float));

    api->ReleaseTensorTypeAndShapeInfo(shapeInfo);
    api->ReleaseValue(outputTensor);

    *outData = data;
    *outShape = dims;
    *outDims = (int)numDims;
    return NULL;
}

void ort_free_result(float* data, int64_t* shape) {
    free(data);
    free(shape);
}