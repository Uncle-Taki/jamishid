import { SearchCriteria, ScoredListing } from '../types/market';

const API_URL = process.env.REACT_APP_API_URL || 'http://localhost:8080/api/v1';

export const fetchLiveListings = async (criteria: SearchCriteria, isMockMode: boolean = false): Promise<ScoredListing[]> => {
    if (isMockMode) {
        // Fallback to internal mock generator if needed
        return [];
    }

    try {
        const response = await fetch(`${API_URL}/crawl`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(criteria)
        });

        if (!response.ok) {
            throw new Error(`API Error: ${response.status}`);
        }

        const data = await response.json();
        if (data.success) {
            return data.data;
        } else {
            throw new Error(`API Error: ${data.error}`);
        }
    } catch (error) {
        console.error("Failed to fetch listings:", error);
        return [];
    }
};
