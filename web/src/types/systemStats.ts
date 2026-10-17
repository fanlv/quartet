export interface SystemStats {
  hostname: string;
  sampledAt: string;
  cpu: {
    model: string;
    logicalCores: number;
    usagePercent: number;
  } | null;
  memory: SystemCapacityStats | null;
  disk: (SystemCapacityStats & { path: string }) | null;
  errors: string[];
}

export interface SystemCapacityStats {
  totalBytes: number;
  usedBytes: number;
  availableBytes: number;
  usagePercent: number;
}
