"use server";

import { fetchApi, createErrorResponse } from "./utils";
import { BaseResponse } from "@/types";

/** Lightweight context item listing entry (chat @-mention autocomplete). */
export interface ClusterResourceItem {
  kind: string;
  namespace?: string;
  name: string;
  /** Provider-specific parent scope (e.g. the Jenkins job of a build). */
  scope?: string;
  status?: string;
  /** Context provider the item belongs to; defaults to "kubernetes". */
  provider?: string;
}

/** Injectable context text for one resource. */
export interface ClusterResourceContext {
  provider: string;
  kind: string;
  namespace?: string;
  scope?: string;
  name: string;
  text: string;
}

/**
 * Lists cluster resources of a kind for the @-mention picker.
 */
export async function listClusterResources(
  kind: string,
  options: { namespace?: string; query?: string; limit?: number } = {}
): Promise<BaseResponse<ClusterResourceItem[]>> {
  try {
    const params = new URLSearchParams({ kind });
    if (options.namespace) params.set("namespace", options.namespace);
    if (options.query) params.set("query", options.query);
    if (options.limit) params.set("limit", String(options.limit));

    const response = await fetchApi<BaseResponse<ClusterResourceItem[]>>(
      `/cluster/resources?${params.toString()}`
    );
    if (!response) {
      throw new Error("Failed to list cluster resources");
    }
    return { message: "Resources listed successfully", data: response.data ?? [] };
  } catch (error) {
    return createErrorResponse<ClusterResourceItem[]>(error, "Error listing cluster resources");
  }
}

/** Lists the configured context providers (kubernetes always; jenkins etc. when enabled). */
export async function listContextProviders(): Promise<BaseResponse<string[]>> {
  try {
    const response = await fetchApi<BaseResponse<string[]>>("/context/providers");
    return { message: "Providers listed successfully", data: response?.data ?? ["kubernetes"] };
  } catch (error) {
    return createErrorResponse<string[]>(error, "Error listing context providers");
  }
}

/** Lists Jenkins jobs (kind=job) or builds of a job (kind=build). */
export async function listJenkinsResources(
  kind: "job" | "build",
  options: { job?: string; query?: string; limit?: number } = {}
): Promise<BaseResponse<ClusterResourceItem[]>> {
  try {
    const params = new URLSearchParams({ kind });
    if (options.job) params.set("job", options.job);
    if (options.query) params.set("query", options.query);
    if (options.limit) params.set("limit", String(options.limit));

    const response = await fetchApi<BaseResponse<ClusterResourceItem[]>>(
      `/context/jenkins/resources?${params.toString()}`
    );
    const items = (response?.data ?? []).map(item => ({ ...item, provider: "jenkins" }));
    return { message: "Jenkins resources listed successfully", data: items };
  } catch (error) {
    return createErrorResponse<ClusterResourceItem[]>(error, "Error listing Jenkins resources");
  }
}

/** Fetches the injectable context text for a Jenkins job or build. */
export async function getJenkinsResourceContext(
  kind: string,
  name: string,
  job?: string
): Promise<BaseResponse<ClusterResourceContext>> {
  try {
    const params = new URLSearchParams({ kind, name });
    if (job) params.set("job", job);

    const response = await fetchApi<BaseResponse<ClusterResourceContext>>(
      `/context/jenkins/resources/context?${params.toString()}`
    );
    if (!response?.data) {
      throw new Error("Failed to fetch Jenkins context");
    }
    return { message: "Jenkins context fetched successfully", data: response.data };
  } catch (error) {
    return createErrorResponse<ClusterResourceContext>(error, "Error fetching Jenkins context");
  }
}

/**
 * Fetches the injectable context text for one resource.
 */
export async function getClusterResourceContext(
  kind: string,
  name: string,
  namespace?: string
): Promise<BaseResponse<ClusterResourceContext>> {
  try {
    const params = new URLSearchParams({ kind, name });
    if (namespace) params.set("namespace", namespace);

    const response = await fetchApi<BaseResponse<ClusterResourceContext>>(
      `/cluster/resources/context?${params.toString()}`
    );
    if (!response?.data) {
      throw new Error("Failed to fetch resource context");
    }
    return { message: "Resource context fetched successfully", data: response.data };
  } catch (error) {
    return createErrorResponse<ClusterResourceContext>(error, "Error fetching resource context");
  }
}
