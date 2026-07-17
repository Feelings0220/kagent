"use server";

import { fetchApi, createErrorResponse } from "./utils";
import { BaseResponse } from "@/types";

/** Lightweight cluster resource listing entry (chat @-mention autocomplete). */
export interface ClusterResourceItem {
  kind: string;
  namespace?: string;
  name: string;
  status?: string;
}

/** Injectable context text for one cluster resource. */
export interface ClusterResourceContext {
  provider: string;
  kind: string;
  namespace?: string;
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
